package httpapi

import (
	"slices"
	"strconv"
	"strings"
)

// listSpecification documents the unified list parameters (G08.2, G49.1) on
// every registered list operation present in paths. Parameters an operation
// already declares keep their more specific description; the missing ones
// are added from the contract. x-jelee-list records the contract itself.
func listSpecification(paths, schemas map[string]any) {
	schemas["ListPagination"] = objectSchema(map[string]any{
		"nextCursor": map[string]any{"type": "string", "description": "Pass as cursor for the next page; empty on the last page."},
		"limit":      map[string]any{"type": "integer", "minimum": 1, "description": "Absent when every row was returned."},
		"offset":     map[string]any{"type": "integer", "minimum": 0},
		"total":      map[string]any{"type": "integer", "minimum": 0},
	}, "nextCursor", "offset", "total")
	for path, c := range listContracts {
		item, ok := paths[path].(map[string]any)
		if !ok {
			continue
		}
		op, ok := item["get"].(map[string]any)
		if !ok {
			continue
		}
		existing, _ := op["parameters"].([]any)
		params := slices.Clone(existing)
		declared := map[string]bool{}
		for _, raw := range params {
			if p, ok := raw.(map[string]any); ok && p["in"] == "query" {
				declared[p["name"].(string)] = true
			}
		}
		for _, name := range listStandardParameters {
			if !declared[name] {
				params = append(params, c.parameter(name))
			}
		}
		op["parameters"] = params
		op["x-jelee-list"] = map[string]any{"paging": string(c.Paging), "fields": c.Fields, "sort": c.Sorts, "order": c.Orders, "filters": append([]string{}, c.Filters...), "maxLimit": c.MaxLimit, "offsetMax": c.OffsetMax}
		c.responsePagination(op, schemas)
	}
}

func (c *listContract) parameter(name string) map[string]any {
	query := func(description string, schema map[string]any) map[string]any {
		return map[string]any{"name": name, "in": "query", "description": description, "schema": schema}
	}
	switch name {
	case "cursor":
		if c.Paging == pagingKeyset {
			return query("Opaque cursor: nextCursor of the previous page. Not combinable with offset.", map[string]any{"type": "string", "maxLength": 4096})
		}
		return query("Opaque cursor: nextCursor of the previous page (it encodes a position). Not combinable with offset.", map[string]any{"type": "string", "pattern": "^o\\.(0|[1-9][0-9]{0,6})$"})
	case "offset":
		description := "Rows to skip from the start. Not combinable with cursor."
		if c.Paging == pagingKeyset {
			description = "Compatibility paging: rows to skip from the start, walked through cursor pages (at most " + strconv.Itoa(c.OffsetMax) + "); prefer cursor. Not combinable with cursor."
		}
		return query(description, map[string]any{"type": "integer", "minimum": 0, "maximum": c.OffsetMax, "default": 0})
	case "limit":
		schema := map[string]any{"type": "integer", "minimum": 1, "maximum": c.MaxLimit}
		if c.DefaultLimit > 0 {
			schema["default"] = c.DefaultLimit
			return query("Page size.", schema)
		}
		return query("Page size; without it every row is returned.", schema)
	case "sort":
		schema := map[string]any{"type": "string", "enum": c.Sorts}
		if c.Paging == pagingMemory {
			return query("Sort key from the whitelist; without it the storage order is kept. Ties keep the storage order.", schema)
		}
		schema["default"] = c.Sorts[0]
		return query("Sort key. This list is read in storage order, so the whitelist holds only that key.", schema)
	case "order":
		schema := map[string]any{"type": "string", "enum": c.Orders, "default": c.Orders[0]}
		if c.Paging == pagingMemory {
			return query("Direction of sort; requires sort.", schema)
		}
		return query("Direction of sort.", schema)
	}
	description := "Comma separated row members to return: " + strings.Join(c.Fields, ", ") + "."
	if id := c.id(); id != "" {
		description += " " + id + " is always included."
	}
	description += " Unknown names are 400 invalid_request. Selection only removes members of the rows after every visibility rule and mask applied; other required members may then be absent."
	return query(description, map[string]any{"type": "string", "maxLength": 1024})
}

// responsePagination adds the pagination a memory list writes, and the
// offset cursor of an offset list, to the 200 schema.
func (c *listContract) responsePagination(op, schemas map[string]any) {
	if c.Paging == pagingKeyset {
		return
	}
	responses, _ := op["responses"].(map[string]any)
	ok200, _ := responses["200"].(map[string]any)
	content, _ := ok200["content"].(map[string]any)
	media, _ := content["application/json"].(map[string]any)
	schema, _ := media["schema"].(map[string]any)
	if schema == nil {
		return
	}
	target := schema
	if c.Items != "" {
		properties, _ := schema["properties"].(map[string]any)
		target, _ = properties["data"].(map[string]any)
		if ref, ok := target["$ref"].(string); ok {
			target, _ = schemas[strings.TrimPrefix(ref, "#/components/schemas/")].(map[string]any)
		}
	}
	properties, _ := target["properties"].(map[string]any)
	if properties == nil {
		return
	}
	name, value := "pagination", any(schemaRef("ListPagination"))
	if c.Paging == pagingOffset {
		name, value = "nextCursor", map[string]any{"type": "string", "description": "Pass as cursor for the next page; empty on the last page."}
	}
	if _, ok := properties[name]; ok {
		return
	}
	properties[name] = value
	switch required := target["required"].(type) {
	case []any:
		target["required"] = append(slices.Clone(required), name)
	case []string:
		target["required"] = append(slices.Clone(required), name)
	default:
		target["required"] = []string{name}
	}
}
