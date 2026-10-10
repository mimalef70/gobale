package rest

import (
	"github.com/gofiber/fiber/v3"
	"maps"

	"github.com/mimalef70/goomni/src/domains"
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

func publicCapabilities(list []domains.OperationContract) []domains.OperationContract {
	for i, c := range list {
		if _, hasName := c.Request.Properties["name"]; c.Operation == "account.name" && hasName {
			c.Request.Properties = maps.Clone(c.Request.Properties)
			c.Request.Properties["push_name"] = c.Request.Properties["name"]
			delete(c.Request.Properties, "name")
			c.Request.Required = []string{"push_name"}
			list[i] = c
		}
	}
	return list
}
func (s *Server) capabilities(c fiber.Ctx) error {
	p := domains.Provider(c.Query("provider"))
	if err := p.Validate(); err != nil {
		return err
	}
	list, err := s.service.Capabilities(p)
	return result(c, publicCapabilities(list), err)
}
