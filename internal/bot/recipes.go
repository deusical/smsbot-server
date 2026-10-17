package bot

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

func strictRecipe(raw []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil || d.Decode(new(any)) != io.EOF {
		return errors.New("invalid_recipe")
	}
	return nil
}

// ImportRecipeJSON accepts server configuration recipes. A recipe carries no
// keys or enrolled identities.
func (e *Engine) ImportRecipeJSON(raw []byte) error {
	var c Configuration
	if err := strictRecipe(raw, &c); err != nil {
		return err
	}
	return e.ImportRecipe(c)
}
