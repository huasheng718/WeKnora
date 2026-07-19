package types

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode"
)

var ErrProductionCredentialField = errors.New("credential fields are not allowed in production snapshots")

// NormalizeProductionCredentialKey removes case and separator differences but
// preserves word content, allowing exact credential-name matching without
// treating legitimate names such as tokenized or passwordless as secrets.
func NormalizeProductionCredentialKey(key string) string {
	var normalized strings.Builder
	for _, r := range strings.ToLower(key) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			normalized.WriteRune(r)
		}
	}
	return normalized.String()
}

func IsProductionCredentialKey(key string) bool {
	switch NormalizeProductionCredentialKey(key) {
	case "token", "accesstoken", "refreshtoken", "clientsecret", "apikey", "authorization",
		"password", "secret", "privatekey", "credential", "credentials", "appsecret", "authheaders",
		"accesskey", "passwd":
		return true
	default:
		return false
	}
}

func RejectProductionCredentialFields(raw JSON) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values are not allowed")
		}
		return err
	}
	var inspect func(any) error
	inspect = func(candidate any) error {
		switch typed := candidate.(type) {
		case map[string]any:
			for key, nested := range typed {
				if IsProductionCredentialKey(key) {
					return ErrProductionCredentialField
				}
				if err := inspect(nested); err != nil {
					return err
				}
			}
		case []any:
			for _, nested := range typed {
				if err := inspect(nested); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return inspect(value)
}
