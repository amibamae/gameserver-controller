package main

import (
	"encoding/json"
	"fmt"
)

// Register the hosting catalog evaluators defined in this file.
func init() {
	registerEvaluator(evaluateHostingCatalogList,
		"hosting.regions.list.filtered",
		"hosting.plans.list.resource-filter",
		"hosting.plans.list.minimum-filter-mismatch",
		"hosting.os_offerings.list.unknown-not-available")
}

// evaluateHostingCatalogList evaluates the conformance fixtures hosting.regions.list.filtered,
// hosting.plans.list.resource-filter, hosting.plans.list.minimum-filter-mismatch,
// hosting.os_offerings.list.unknown-not-available.
func evaluateHostingCatalogList(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	kind := stringValue(input, "catalog_kind")
	catalogInput := asObject(input["catalog_input"])
	catalogOutput := asObject(input["catalog_output"])
	inputSchemaID, outputSchemaID, itemsKey, itemIDKey := catalogShape(kind)
	if err := pkg.validateSchema(inputSchemaID, catalogInput); err != nil {
		return nil, fmt.Errorf("hosting %s list input does not match schema: %w", kind, err)
	}
	if err := pkg.validateSchema(outputSchemaID, catalogOutput); err != nil {
		return nil, fmt.Errorf("hosting %s list output does not match schema: %w", kind, err)
	}
	items := arrayValue(catalogOutput, itemsKey)
	paginationContinues := stringValue(catalogOutput, "next_page_token") != ""
	filterMatches, uniqueItems, eligible := true, true, len(items) > 0
	seen := make(map[string]bool)
	for _, raw := range items {
		item := asObject(raw)
		key := stringValue(item, itemIDKey)
		if kind == "plans" || kind == "os_offerings" {
			key = stringValue(item, "region_id") + "\x00" + key
		}
		if seen[key] {
			uniqueItems = false
		}
		seen[key] = true
		if !catalogItemMatchesFilter(kind, catalogInput, item) {
			filterMatches = false
		}
		if stringValue(item, "availability") != "available" {
			eligible = false
		}
	}
	pageSize, ok := catalogNumber(catalogInput, "page_size")
	withinPageSize := !ok || float64(len(items)) <= pageSize
	return map[string]any{
		"schemas_valid":              true,
		"request_filter_matches":     filterMatches,
		"items_unique":               uniqueItems,
		"within_page_size":           withinPageSize,
		"read_only":                  true,
		"all_items_report_available": eligible,
		"pagination_continues":       paginationContinues,
	}, nil
}

// catalogShape returns the schemas and item keys of a hosting catalog
// listing. An unknown kind returns empty values, so schema validation
// fails.
func catalogShape(kind string) (inputSchemaID, outputSchemaID, itemsKey, itemIDKey string) {
	switch kind {
	case "regions":
		return "urn:gameserver-controller:0.21.0-draft:hosting:regions-list-input",
			"urn:gameserver-controller:0.21.0-draft:hosting:regions-list-output", "regions", "region_id"
	case "plans":
		return "urn:gameserver-controller:0.21.0-draft:hosting:plans-list-input",
			"urn:gameserver-controller:0.21.0-draft:hosting:plans-list-output", "plans", "plan_id"
	case "os_offerings":
		return "urn:gameserver-controller:0.21.0-draft:hosting:os-offerings-list-input",
			"urn:gameserver-controller:0.21.0-draft:hosting:os-offerings-list-output", "os_offerings", "offering_id"
	}
	return "", "", "", ""
}

// catalogNumber reads a JSON number field; ok is false when it is absent
// or not a valid number.
func catalogNumber(object map[string]any, key string) (float64, bool) {
	raw, ok := object[key].(json.Number)
	if !ok {
		return 0, false
	}
	value, err := raw.Float64()
	return value, err == nil
}

// catalogItemMatchesFilter: the item satisfies the request's region filter
// and, for plans, the minimum CPU and memory, and for OS offerings, the
// requested family and architecture.
func catalogItemMatchesFilter(kind string, request, item map[string]any) bool {
	matches := true
	if kind == "regions" {
		regionIDs := arrayValue(request, "region_ids")
		if len(regionIDs) > 0 && !contains(regionIDs, stringValue(item, "region_id")) {
			matches = false
		}
	} else if regionID := stringValue(request, "region_id"); regionID != "" && stringValue(item, "region_id") != regionID {
		matches = false
	}
	switch kind {
	case "plans":
		for _, minimum := range [][2]string{{"minimum_vcpu_count", "vcpu_count"}, {"minimum_memory_mib", "memory_mib"}} {
			if want, ok := catalogNumber(request, minimum[0]); ok {
				if actual, ok := catalogNumber(item, minimum[1]); !ok || actual < want {
					matches = false
				}
			}
		}
	case "os_offerings":
		for _, field := range []string{"os_family", "architecture"} {
			if requested := stringValue(request, field); requested != "" && stringValue(item, field) != requested {
				matches = false
			}
		}
	}
	return matches
}
