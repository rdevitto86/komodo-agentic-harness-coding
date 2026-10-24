package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateSchema(t *testing.T) {
	const schema = `{
	  "type":"object","required":["status","items"],"additionalProperties":false,
	  "properties":{
	    "status":{"type":"string","enum":["pass","fail"]},
	    "count":{"type":"integer","minimum":0,"maximum":10},
	    "note":{"type":"string","minLength":2,"maxLength":5,"pattern":"^[a-z]+$"},
	    "items":{"type":"array","minItems":1,"maxItems":2,"items":{"$ref":"#/$defs/item"}},
	    "either":{"anyOf":[{"type":"string"},{"type":"null"}]}
	  },
	  "$defs":{"item":{"type":"object","required":["id"],"properties":{"id":{"type":["string","integer"]}}}}
	}`
	good := `{"status":"pass","count":3,"note":"ok","items":[{"id":"a"},{"id":2}],"either":null}`
	if err := validateSchema(json.RawMessage(schema), json.RawMessage(good)); err != nil {
		t.Fatalf("good document: %v", err)
	}
	bad := map[string]string{
		`{"items":[{"id":"a"}]}`:                                   "required field",
		`{"status":"maybe","items":[{"id":"a"}]}`:                  "allowed values",
		`{"status":"pass","items":[]}`:                             "at least",
		`{"status":"pass","items":[{"id":"a"},{"id":1},{"id":2}]}`: "at most",
		`{"status":"pass","items":[{"id":true}]}`:                  "want string or integer",
		`{"status":"pass","items":[{}]}`:                           "required field",
		`{"status":"pass","items":[{"id":"a"}],"extra":1}`:         "unexpected field",
		`{"status":"pass","items":[{"id":"a"}],"count":11}`:        "above",
		`{"status":"pass","items":[{"id":"a"}],"count":1.5}`:       "want integer",
		`{"status":"pass","items":[{"id":"a"}],"note":"A1"}`:       "pattern",
		`{"status":"pass","items":[{"id":"a"}],"note":"toolong"}`:  "longer",
		`{"status":"pass","items":[{"id":"a"}],"either":5}`:        "anyOf",
		`[]`: "want object",
	}
	for doc, want := range bad {
		err := validateSchema(json.RawMessage(schema), json.RawMessage(doc))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want it to mention %q", doc, err, want)
		}
	}
	if err := validateSchema(json.RawMessage(`{`), json.RawMessage(`{}`)); err == nil {
		t.Error("a broken schema must fail")
	}
}
