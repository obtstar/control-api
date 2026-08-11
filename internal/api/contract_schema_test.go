// OpenAPI 契约加载与最小响应 schema 校验器（仅测试使用，不进二进制）。
// 能力边界：仅支持 type string/integer/boolean/array/object、required、enum、
// type [X,"null"] 联合类型、指向 #/components/schemas/ 的本地 $ref；
// 不校验 additionalProperties（多余字段放行）/format/数值范围等其余关键字。
package api

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	"gopkg.in/yaml.v3"
)

// contractPath 相对本包测试工作目录（internal/api）的契约唯一可信源
const contractPath = "../../docs/api/openapi.yaml"

type contract struct {
	doc     map[string]any
	schemas map[string]any
}

var loadContractOnce = sync.OnceValues(func() (*contract, error) {
	data, err := os.ReadFile(contractPath)
	if err != nil {
		return nil, fmt.Errorf("读取契约 %s: %w", contractPath, err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("解析契约 %s: %w", contractPath, err)
	}
	comp, _ := doc["components"].(map[string]any)
	schemas, _ := comp["schemas"].(map[string]any)
	if schemas == nil {
		return nil, fmt.Errorf("契约 %s 缺 components.schemas", contractPath)
	}
	return &contract{doc: doc, schemas: schemas}, nil
})

func contractSpec(t *testing.T) *contract {
	t.Helper()
	c, err := loadContractOnce()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// responseSchema 取 paths[path].method.responses[status] 的 application/json schema。
// pattern 为 Go mux 路径（含 /api 前缀），按 servers[0].url 前缀剥离后查 paths。
func (c *contract) responseSchema(method, pattern string, status int) (any, error) {
	prefix := ""
	if servers, ok := c.doc["servers"].([]any); ok && len(servers) > 0 {
		prefix, _ = servers[0].(map[string]any)["url"].(string)
	}
	path := strings.TrimPrefix(pattern, prefix)
	paths, _ := c.doc["paths"].(map[string]any)
	item, ok := paths[path].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("契约无路径: %s", path)
	}
	op, ok := item[strings.ToLower(method)].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("契约无操作: %s %s", method, path)
	}
	resp, ok := op["responses"].(map[string]any)[fmt.Sprintf("%d", status)].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("契约无响应定义: %s %s %d", method, path, status)
	}
	content, _ := resp["content"].(map[string]any)["application/json"].(map[string]any)
	if content == nil || content["schema"] == nil {
		return nil, fmt.Errorf("契约无 JSON schema: %s %s %d", method, path, status)
	}
	return content["schema"], nil
}

// validateJSON 校验响应 body 符合契约中 method+pattern+status 的 schema
func (c *contract) validateJSON(t *testing.T, method, pattern string, status int, body []byte) {
	t.Helper()
	schema, err := c.responseSchema(method, pattern, status)
	if err != nil {
		t.Fatal(err)
	}
	var data any
	if err := json.Unmarshal(body, &data); err != nil {
		t.Fatalf("响应非 JSON: %v: %s", err, body)
	}
	if err := c.check(data, schema, "$"); err != nil {
		t.Fatalf("契约校验失败 %s %s %d: %v\n响应: %s", method, pattern, status, err, body)
	}
}

// check 递归校验：$ref 解析 → null 联合 → 非 null 类型逐一尝试
func (c *contract) check(value, raw any, path string) error {
	schema, ok := raw.(map[string]any)
	if !ok {
		return fmt.Errorf("%s: schema 非对象", path)
	}
	if ref, ok := schema["$ref"].(string); ok {
		const p = "#/components/schemas/"
		if !strings.HasPrefix(ref, p) {
			return fmt.Errorf("%s: 不支持的 $ref（仅本地 components/schemas）: %s", path, ref)
		}
		target, ok := c.schemas[ref[len(p):]]
		if !ok {
			return fmt.Errorf("%s: $ref 目标不存在: %s", path, ref)
		}
		return c.check(value, target, path)
	}
	if value == nil {
		for _, ty := range typeSet(schema["type"]) {
			if ty == "null" {
				return nil
			}
		}
		return fmt.Errorf("%s: 值为 null，但 type=%v 不允许", path, schema["type"])
	}
	var lastErr error
	for _, ty := range typeSet(schema["type"]) {
		if ty == "null" {
			continue
		}
		if lastErr = c.checkType(value, schema, ty, path); lastErr == nil {
			return nil
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("%s: schema 无可用 type", path)
	}
	return lastErr
}

func typeSet(raw any) []string {
	switch v := raw.(type) {
	case string:
		return []string{v}
	case []any:
		out := make([]string, 0, len(v))
		for _, e := range v {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func (c *contract) checkType(value, schema any, ty, path string) error {
	switch ty {
	case "string":
		if _, ok := value.(string); !ok {
			return fmt.Errorf("%s: 类型错误，want string, got %T", path, value)
		}
	case "integer":
		f, ok := value.(float64)
		if !ok || f != math.Trunc(f) {
			return fmt.Errorf("%s: 类型错误，want integer, got %v", path, value)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("%s: 类型错误，want boolean, got %T", path, value)
		}
	case "array":
		arr, ok := value.([]any)
		if !ok {
			return fmt.Errorf("%s: 类型错误，want array, got %T", path, value)
		}
		for i, e := range arr {
			if err := c.check(e, schema.(map[string]any)["items"], fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	case "object":
		obj, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: 类型错误，want object, got %T", path, value)
		}
		if err := c.checkObject(obj, schema.(map[string]any), path); err != nil {
			return err
		}
	default:
		return fmt.Errorf("%s: 校验器不支持的 type: %s", path, ty)
	}
	return checkEnum(value, schema.(map[string]any), path)
}

func (c *contract) checkObject(obj, schema map[string]any, path string) error {
	for _, req := range typeSet(schema["required"]) {
		if _, ok := obj[req]; !ok {
			return fmt.Errorf("%s: 缺 required 字段 %q", path, req)
		}
	}
	props, _ := schema["properties"].(map[string]any)
	for k, ps := range props {
		if v, ok := obj[k]; ok {
			if err := c.check(v, ps, path+"."+k); err != nil {
				return err
			}
		}
	}
	return nil
}

func checkEnum(value any, schema map[string]any, path string) error {
	enum, ok := schema["enum"].([]any)
	if !ok {
		return nil
	}
	for _, e := range enum {
		if reflect.DeepEqual(value, e) {
			return nil
		}
	}
	return fmt.Errorf("%s: 值 %v 不在 enum %v 内", path, value, enum)
}

// ── 校验器自测（表驱动）─────────────────────────────────────

func TestSchemaValidator(t *testing.T) {
	parse := func(y string) any {
		t.Helper()
		var v any
		if err := yaml.Unmarshal([]byte(y), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	obj := `type: object
required: [name]
properties:
  name: {type: string}
  age: {type: integer}
  tag: {type: [string, "null"]}
  role: {type: string, enum: [admin, team]}
`
	cases := []struct {
		name    string
		schema  any
		data    string
		wantErr bool
	}{
		{"合法对象", parse(obj), `{"name":"a","age":3,"tag":null,"role":"admin"}`, false},
		{"可选字段可缺席", parse(obj), `{"name":"a"}`, false},
		{"缺 required", parse(obj), `{"age":3}`, true},
		{"类型错误", parse(obj), `{"name":"a","age":"3"}`, true},
		{"integer 非整", parse(obj), `{"name":"a","age":3.5}`, true},
		{"enum 越界", parse(obj), `{"name":"a","role":"root"}`, true},
		{"null 联合允许", parse(obj), `{"name":"a","tag":null}`, false},
		{"null 非联合拒绝", parse(obj), `{"name":null}`, true},
		{"数组元素校验", parse(`type: array
items: {type: string}`), `["a","b"]`, false},
		{"数组元素类型错", parse(`type: array
items: {type: string}`), `["a",1]`, true},
		{"boolean", parse(`{type: boolean}`), `true`, false},
		{"boolean 类型错", parse(`{type: boolean}`), `"true"`, true},
		{"未知 type 报错", parse(`{type: float}`), `1.5`, true},
		{"本地 $ref", parse(`{$ref: '#/components/schemas/Error'}`), `{"error":"x"}`, false},
		{"本地 $ref 缺 required", parse(`{$ref: '#/components/schemas/Error'}`), `{}`, true},
		{"外部 $ref 不支持", parse(`{$ref: './other.yaml#/X'}`), `{}`, true},
		{"$ref 目标不存在", parse(`{$ref: '#/components/schemas/Nope'}`), `{}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var data any
			if err := json.Unmarshal([]byte(tc.data), &data); err != nil {
				t.Fatal(err)
			}
			err := contractSpec(t).check(data, tc.schema, "$")
			if tc.wantErr && err == nil {
				t.Fatalf("want error, got nil（data=%s）", tc.data)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("want nil, got %v（data=%s）", err, tc.data)
			}
		})
	}
}
