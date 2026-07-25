package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const DefaultCookieFile = ".cli-cookie"

type CookieData struct {
	User  string `json:"user,omitempty"`
	Token string `json:"token,omitempty"`
}

type CardPhoto struct {
	Type string `json:"type,omitempty"`
	Data string `json:"data,omitempty"`
}

type Card struct {
	Fn    string     `json:"fn,omitempty"`
	Photo *CardPhoto `json:"photo,omitempty"`
	Note  string     `json:"note,omitempty"`
}

// MakeTheCard creates a JSON string for user public profile.
func MakeTheCard(fn, photoPath, note string) (string, error) {
	card := Card{
		Fn:   fn,
		Note: note,
	}

	if photoPath != "" {
		if strings.HasPrefix(photoPath, "http://") || strings.HasPrefix(photoPath, "https://") {
			card.Photo = &CardPhoto{
				Type: "url",
				Data: photoPath,
			}
		} else {
			data, err := os.ReadFile(photoPath)
			if err != nil {
				return "", fmt.Errorf("failed to read photo file %s: %w", photoPath, err)
			}
			ext := strings.TrimPrefix(filepath.Ext(photoPath), ".")
			if ext == "" {
				ext = "jpg"
			}
			card.Photo = &CardPhoto{
				Type: ext,
				Data: base64.StdEncoding.EncodeToString(data),
			}
		}
	}

	b, err := json.Marshal(card)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// SaveCookie saves token and user to cookie file.
func SaveCookie(filename, user, token string) error {
	if filename == "" {
		filename = DefaultCookieFile
	}
	c := CookieData{
		User:  user,
		Token: token,
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filename, data, 0600)
}

// ReadCookie reads token and user from cookie file.
func ReadCookie(filename string) (*CookieData, error) {
	if filename == "" {
		filename = DefaultCookieFile
	}
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}
	var c CookieData
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// ParseCred parses "scheme:value" or "email:alice@example.com".
func ParseCred(credStr string) (string, string) {
	parts := strings.SplitN(credStr, ":", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "email", credStr
}

// PrettyJSON formats an object or json bytes into indented JSON.
func PrettyJSON(v interface{}) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

// SplitArgs splits command line into tokens taking single/double quotes into account.
func SplitArgs(input string) []string {
	var args []string
	var current strings.Builder
	inQuotes := false
	quoteChar := rune(0)

	for _, r := range input {
		switch {
		case inQuotes:
			if r == quoteChar {
				inQuotes = false
			} else {
				current.WriteRune(r)
			}
		case r == '\'' || r == '"':
			inQuotes = true
			quoteChar = r
		case r == ' ' || r == '\t':
			if current.Len() > 0 {
				args = append(args, current.String())
				current.Reset()
			}
		default:
			current.WriteRune(r)
		}
	}
	if current.Len() > 0 {
		args = append(args, current.String())
	}
	return args
}
