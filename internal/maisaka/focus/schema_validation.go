package focus

import (
	"errors"
	"fmt"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

const toolArgumentSchemaResource = "focus://tool-arguments.json"

// validateToolArguments 用目标声明的 JSON Schema 校验一次工具参数
func validateToolArguments(arguments map[string]any, schema map[string]any) error {
	if len(schema) == 0 {
		// Python 侧把空声明视为 {"type": "object"}，这里保持一致
		schema = map[string]any{"type": "object"}
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource(toolArgumentSchemaResource, schema); err != nil {
		return fmt.Errorf("加载参数声明失败: %w", err)
	}
	compiled, err := compiler.Compile(toolArgumentSchemaResource)
	if err != nil {
		return fmt.Errorf("编译参数声明失败: %w", err)
	}
	return compiled.Validate(arguments)
}

// validationMessage 取最内层校验错误，尽量贴近 Python jsonschema 的 exc.message
func validationMessage(err error) string {
	var validationErr *jsonschema.ValidationError
	if !errors.As(err, &validationErr) {
		return err.Error()
	}
	leaf := validationLeaf(validationErr)
	if leaf == nil || leaf.ErrorKind == nil {
		return validationErr.Error()
	}
	return leaf.ErrorKind.LocalizedString(message.NewPrinter(language.English))
}

func validationLeaf(err *jsonschema.ValidationError) *jsonschema.ValidationError {
	if err == nil {
		return nil
	}
	if len(err.Causes) == 0 {
		return err
	}
	for _, cause := range err.Causes {
		if leaf := validationLeaf(cause); leaf != nil {
			return leaf
		}
	}
	return nil
}
