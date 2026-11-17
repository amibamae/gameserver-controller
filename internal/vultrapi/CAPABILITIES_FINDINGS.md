# Vultr API Capabilities Findings

## Overview

This document summarizes the capabilities and schemas discovered in the existing `internal/vultrapi/` package. The code implements a client for Vultr's Compute Instances API v2.

---

## Core Instance Operations

### 1. **Create Instance**
- **Endpoint**: `POST /v2/instances`
- **Request Body**:
```json
{
  "region": "ewr",
  "plan": "vc2-1c-2gb",
  "os_id": 362,
  "label": "gsc-pilot-staging-001",
  "hostname": "staging-001.example.invalid",
  "sshkey_id": ["sshkey-123"],
  "script_id": "script-123",
  "user_data": "base64-encoded-data",
  "firewall_group_id": "fw-group-123",
  "tags": ["managed-by-gsc", "environment-staging"]
}
```
- **Response**: `{"instance": {...}}`
- **Schema**: See `client.go:Instance`, `CreateInstanceRequest`, `createInstanceBody`

### 2. **Get Instance**
- **Endpoint**: `GET /v2/instances/{instance_id}`
- **Response**: `{"instance": {...}}`
- **Fields Returned**: id, date_created, region, plan, os_id, os, label, hostname, status, power_status, main_ip, tags

### 3. **List Instances**
- **Endpoint**: `GET /v2/instances?label={L}&per_page=100&cursor={C}`
- **Response**: 
```json
{
  "instances": [{"id": "...", ...}],
  "meta": {
    "total": 42,
    "links": {"next": "cursor-value"}
  }
}
```

### 4. **List Instances by Label**
- **Endpoint**: Same as above with `label` query parameter
- **Behavior**: Filters results to exact label match; paginated

---

## Startup Scripts

### 5. **Create Startup Script**
- **Endpoint**: `POST /v2/startup-scripts`
- **Request Body**:
```json
{
  "name": "bootstrap-script",
  "type": "boot",
  "script": "base64-encoded-script-content"
}
```
- **Response**: `{"startup_script": {...}}`
- **Schema**: See `startup_scripts.go:StartupScript`, `StartupScriptRequest`
- **Type Enum**: `"boot"` or `"pxe"`
- **Max Size**: 1 MiB

### 6. **Get Startup Script**
- **Endpoint**: `GET /v2/startup-scripts/{script_id}`
- **Response**: `{"startup_script": {...}}`
- **Validation**: Base64-encoded script, UTF-8 text, type must be boot/pxe

### 7. **Update Startup Script**
- **Endpoint**: `PATCH /v2/startup-scripts/{script_id}`
- **Request Body**: Same as create (name, type, script)
- **Response**: `{"startup_script": {...}}` or outcome unknown

---

## SSH Keys

### 8. **Create SSH Key**
- **Endpoint**: `POST /v2/ssh-keys`
- **Request Body**:
```json
{
  "name": "my-key",
  "ssh_key": "ssh-ed25519 AAAA... (public key)"
}
```
- **Response**: `{"ssh_key": {...}}`
- **Schema**: See `ssh_access.go:SSHKey`
- **Name Max Length**: 255 characters

### 9. **Get SSH Key**
- **Endpoint**: `GET /v2/ssh-keys/{key_id}`
- **Response**: `{"ssh_key": {...}}`
- **Fields**: id, date_created, name, ssh_key (public key)

---

## Firewall Rules

### 10. **List Firewall Rules**
- **Endpoint**: `GET /v2/firewalls/{group_id}/rules?per_page=100&cursor={C}`
- **Response**:
```json
{
  "firewall_rules": [
    {
      "id": 1,
      "ip_type": "ipv4",
      "action": "accept",
      "protocol": "tcp",
      "port": "22",
      "subnet": "10.0.0.0",
      "subnet_size": 32,
      "source": ""
    }
  ],
  "meta": {
    "total": 5,
    "links": {"next": "cursor-value"}
  }
}
```
- **Schema**: See `ssh_access.go:FirewallRule`
- **Fields**: id, ip_type, action, protocol, port, subnet, subnet_size, source

### 11. **Firewall Rule Attributes**
- **Action**: `"accept"` (dropped traffic by default for non-matching rules)
- **Protocol**: `"tcp"`, `"udp"`, `"icmp"`, etc.
- **IP Type**: `ipv4` or `ipv6`
- **Subnet Format**: CIDR notation (`10.0.0.0/32`)

---

## Instance Management (via PATCH)

### 12. **Replace Instance User Data**
- **Endpoint**: `PATCH /v2/instances/{instance_id}` with user_data field
- **Request Body**:
```json
{
  "user_data": "base64-encoded-user-data"
}
```

### 13. **Get Instance User Data**
- **Endpoint**: `GET /v2/instances/{instance_id}/user-data` (inferred)

---

## Enums and Constants

### Server Plans
- Format: `{provider}-{class}-{cpu}-memory` (e.g., `vc2-1c-2gb`)
- Prefix: `vc{version}{class}-{cores}{vCPU}-{GB}RAM`
- Example values from test fixtures: `"vc2-1c-2gb"`

### OS IDs
- Format: Positive integer
- Example: `362` (Ubuntu-based images)
- Stored in protocol as string, converted to int for API calls

### Instance Status Values
- Returns as simple string from Vultr API
- Test fixture shows: `"pending"`

### Startup Script Types
- `"boot"`: Boot-time script
- `"pxe"`: PXE boot script

---

## Request/Response JSON Shapes

### Common Envelope Pattern
All single-resource responses use envelope wrappers:
```json
{
  "resource_name": {
    // fields...
  }
}
```

### Pagination Metadata
```json
{
  "instances": [...],
  "meta": {
    "total": <int>,
    "links": {
      "next": "<cursor>""
    }
  }
}
```

---

## Validation Rules

### Resource ID Format
- Pattern: `^[A-Za-z0-9][A-Za-z0-9._-]{0,199}$`
- Examples: `"inst-123"`, `"fw-group-456"`, `"script-789"`

### Label Validation
- Max length: 200 characters
- No control characters (< 0x20 or 0x7f)
- Trimmed (no leading/trailing whitespace)

### Hostname Validation
- Max length: 253 characters
- Valid DNS hostname rules
- No spaces, backslash, tab, CR, LF

### Tag Validation
- Max length: 200 characters per tag
- No duplicates allowed
- Trimmed, no control characters
- Max 100 tags

### User Data (Base64)
- Max decoded size: 64 KiB
- Must be valid base64

---

## Error Handling

### ErrInvalidRequest
- Used for validation failures on client side

### ErrProviderReject
- HTTP 4xx responses from Vultr

### ErrOutcomeUnknown
- HTTP 5xx, timeouts, redirects, or unclear mutation outcomes
- Caller must reconcile before retry

### ErrInvalidResponse
- Malformed or incomplete API responses

---

## API Authentication

- **Header**: `Authorization: Bearer {TOKEN}`
- **Accept Header**: `Accept: application/json`
- **Content-Type**: `application/json` (on POST/PATCH with body)
- Token is bearer token, not API key (Vultr supports webhook-style tokens)

---

## Current Gaps / Missing Operations

Based on the requirements to support `start_server`, `stop_server`, `delete_server`:

### Likely Vultr Endpoints Not Yet Implemented:

1. **Start Instance**
   - Endpoint: `PATCH /v2/instances/{instance_id}`
   - Body: `{"boot_script_id": null}` or status field
   - Need to check Vultr docs for exact payload

2. **Stop Instance**
   - Endpoint: `PATCH /v2/instances/{instance_id}`
   - Body: Status field (e.g., `"status": "shutdown"`)

3. **Delete Instance**
   - Endpoint: `DELETE /v2/instances/{instance_id}`
   - Authenticated delete, no response body or minimal

4. **Reboot Instance**
   - Endpoint: `PATCH /v2/instances/{instance_id}`
   - Body: Reboot action field

---

## Summary of Findings

The existing `internal/vultrapi/` package implements a complete client for Vultr's Compute Instances API with:

| Category | Operations Implemented |
|----------|----------------------|
| **Instances** | Create, Get, List (paginated), Delete (not yet) |
| **Startup Scripts** | Create, Get, Update |
| **SSH Keys** | Create, Get |
| **Firewall Rules** | List rules by group |
| **User Data** | Set via PATCH instance |

### Next Steps:
1. Implement instance lifecycle operations (start/stop/reboot/delete)
2. Add network interface queries if needed
3. Register capabilities with schema registry
4. Complete adapter integration with durable workflow engine
