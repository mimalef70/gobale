package rest

import (
	"maps"

	"github.com/mimalef70/gobale/src/domains"
)

// The native journal keeps its stable argument names for accepted operations.
// Discovery and HTTP use the current public contract, without legacy aliases.
func publicOperationDefinition(name string) (domains.OperationContract, bool) {
	c, ok := domains.OperationDefinition(name)
	if ok && name == "account.name" {
		c.Request.Properties = maps.Clone(c.Request.Properties)
		c.Request.Properties["push_name"] = c.Request.Properties["name"]
		delete(c.Request.Properties, "name")
		c.Request.Required = []string{"push_name"}
	}
	return c, ok
}

func publicOperationDefinitions() []domains.OperationContract {
	list := domains.OperationDefinitions()
	for i := range list {
		list[i], _ = publicOperationDefinition(list[i].Operation)
	}
	return list
}

func nativeOperationArguments(operation string, fields map[string]any) error {
	if operation != "account.name" {
		return nil
	}
	value, ok := fields["push_name"]
	if _, old := fields["name"]; old || !ok {
		return domains.E("INVALID_REQUEST", "push_name is required; name is not a public input field", 400)
	}
	delete(fields, "push_name")
	fields["name"] = value
	return nil
}
