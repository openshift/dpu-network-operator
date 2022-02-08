package api

import (
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// McpReady indicates that the MachineConfig and MachineConfigPool are ready
	McpReady string = "McpReady"

	// TenantObjsSynced indicates that the tenant cm and secrets are synced to infra
	TenantObjsSynced string = "TenantObjsSynced"

	// OvnKubeReady indicates that the ovnkube-node DaemonSet is ready
	OvnKubeReady string = "OvnKubeReady"

	// ReasonCreated is used when desired objects are created
	ReasonCreated = "Created"

	// ReasonCreated is used when desired objects failed to be created
	ReasonFailedCreated = "FailedCreated"

	// ReasonNotFound is used when desired objects is not found
	ReasonNotFound = "NotFound"

	// ReasonProgressing is used when update is progressing
	ReasonProgressing = "Progressing"

	// ReasonCreated is used when desired objects failed to start
	ReasonFailedStart = "FailedStart"
)

type conditionsBuilder struct {
	cndType string
	status  v1.ConditionStatus
	reason  string
	message string
}

func Conditions() *conditionsBuilder {
	return &conditionsBuilder{}
}

func (builder *conditionsBuilder) Build() *v1.Condition {
	return &v1.Condition{
		Type:    builder.cndType,
		Status:  builder.status,
		Reason:  builder.reason,
		Message: builder.message,
	}
}

func (builder *conditionsBuilder) NotTenantObjsSynced() *conditionsBuilder {
	builder.status = v1.ConditionFalse
	builder.cndType = TenantObjsSynced
	return builder
}

func (builder *conditionsBuilder) TenantObjsSynced() *conditionsBuilder {
	builder.status = v1.ConditionTrue
	builder.cndType = TenantObjsSynced
	return builder
}

func (builder *conditionsBuilder) NotMcpReady() *conditionsBuilder {
	builder.status = v1.ConditionFalse
	builder.cndType = McpReady
	return builder
}

func (builder *conditionsBuilder) OvnKubeReady() *conditionsBuilder {
	builder.status = v1.ConditionTrue
	builder.cndType = OvnKubeReady
	return builder
}

func (builder *conditionsBuilder) NotOvnKubeReady() *conditionsBuilder {
	builder.status = v1.ConditionFalse
	builder.cndType = OvnKubeReady
	return builder
}

func (builder *conditionsBuilder) McpReady() *conditionsBuilder {
	builder.status = v1.ConditionTrue
	builder.cndType = McpReady
	return builder
}

func (builder *conditionsBuilder) Reason(r string) *conditionsBuilder {
	builder.reason = r
	return builder
}

func (builder *conditionsBuilder) Msg(msg string) *conditionsBuilder {
	builder.message = msg
	return builder
}
