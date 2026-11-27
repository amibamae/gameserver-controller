package workflow

import (
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/amibamae/gameserver-controller/internal/store"
)

// updateOperation writes next, committing the logical-server record in the
// same write when the operation's definition projects lifecycle state.
func (e *Engine) updateOperation(expectedRevision uint64, next store.OperationRecord) (store.StoredOperation, error) {
	projector, projects, err := e.lifecycleProjectorFor(next.OperationType)
	if err != nil {
		return store.StoredOperation{}, err
	}
	if !projects {
		return e.store.UpdateOperation(expectedRevision, next)
	}
	return e.updateWithLifecycleProjection(expectedRevision, next, projector)
}

// lifecycleProjectorFor returns the registered lifecycle projector for an
// operation type. server.define never projects; any other server.* type must
// have a projector, and other types project only when theirs is one.
func (e *Engine) lifecycleProjectorFor(operationType string) (LifecycleProjector, bool, error) {
	if operationType == "server.define" {
		return nil, false, nil
	}
	definition, _ := e.definitions.get(operationType)
	projector, projects := definition.(LifecycleProjector)
	if projects {
		return projector, true, nil
	}
	if strings.HasPrefix(operationType, "server.") {
		return nil, false, fmt.Errorf("operation %q has no registered lifecycle projector", operationType)
	}
	return nil, false, nil
}

// updateWithLifecycleProjection projects the logical server from next and
// commits both records atomically when the projection is a transition.
func (e *Engine) updateWithLifecycleProjection(expectedRevision uint64, next store.OperationRecord, projector LifecycleProjector) (store.StoredOperation, error) {
	lifecycleStore, ok := e.store.(store.LifecycleOperationStore)
	if !ok {
		return store.StoredOperation{}, errors.New("lifecycle workflow requires an atomic logical-server operation store")
	}
	currentServer, err := lifecycleStore.GetLogicalServer(next.Target.EnvironmentID, next.Target.ServerID)
	if err != nil {
		return store.StoredOperation{}, fmt.Errorf("load logical server before lifecycle projection: %w", err)
	}
	projectedServer, transition, err := projector.ProjectLifecycle(next, currentServer)
	if err != nil {
		return store.StoredOperation{}, fmt.Errorf("project logical-server lifecycle state: %w", err)
	}
	if !transition {
		if !reflect.DeepEqual(projectedServer, currentServer.Record) {
			return store.StoredOperation{}, errors.New("lifecycle projector changed the server record without a state transition")
		}
		return e.store.UpdateOperation(expectedRevision, next)
	}
	updated, _, err := lifecycleStore.UpdateOperationAndLogicalServer(expectedRevision, next, currentServer.Revision, projectedServer)
	if err != nil {
		return store.StoredOperation{}, err
	}
	return updated, nil
}
