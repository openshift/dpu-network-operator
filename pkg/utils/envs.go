package utils

import (
	"os"
)

var TenantNamespace string
var Namespace string

func init() {
	TenantNamespace = os.Getenv("TENANT_NAMESPACE")
	Namespace = os.Getenv("NAMESPACE")
}
