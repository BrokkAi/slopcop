package service

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
)

const dictionaryPath = "/v1/dictionary"

// MaxDictionaryWordBytes is the longest word a workspace dictionary takes.
const MaxDictionaryWordBytes = 64

// dictionaryWordPattern is the engine's word grammar: one identifier part of
// ASCII letters, all lower case, all upper case, or capitalized.
var dictionaryWordPattern = regexp.MustCompile(`^(?:[a-z]+|[A-Z]+|[A-Z][a-z]+)$`)

// ValidDictionaryWord reports whether word is one the workspace dictionary
// takes.
func ValidDictionaryWord(word string) bool {
	return len(word) <= MaxDictionaryWordBytes && dictionaryWordPattern.MatchString(word)
}

// DictionaryAddRequest is the POST /v1/dictionary body: a finding whose
// suggested word to add, or a word.
type DictionaryAddRequest struct {
	FindingID string `json:"findingId,omitempty"`
	Word      string `json:"word,omitempty"`
}

// DictionaryAddResponse is the successful POST /v1/dictionary body. Word is
// the word the dictionary now holds, in lower case; Status is
// DictionaryAdded or DictionaryPresent.
type DictionaryAddResponse struct {
	Word   string `json:"word"`
	Status string `json:"status,omitempty"`
}

// Dictionary add statuses: the word is new, or the workspace had it already.
const (
	DictionaryAdded   = "added"
	DictionaryPresent = "present"
)

// DictionaryList is the GET /v1/dictionary body: the workspace's words,
// alphabetical, the most it may hold, and whether this login may remove
// words.
type DictionaryList struct {
	WorkspaceID string           `json:"workspaceId"`
	Limit       int              `json:"limit"`
	CanRemove   bool             `json:"canRemove"`
	Words       []DictionaryWord `json:"words"`
}

// DictionaryWord is one word of a workspace dictionary. AddedBy is the
// adder's username, nil once that account is erased. SourceFindingID is the
// finding it was added from, nil when it was added as a word. The service
// lowercases words, but treat every field as hostile text all the same.
type DictionaryWord struct {
	Word            string  `json:"word"`
	AddedBy         *string `json:"addedBy"`
	SourceFindingID *string `json:"sourceFindingId"`
	// CreatedAt is an RFC 3339 time.
	CreatedAt string `json:"createdAt"`
}

// AddDictionaryWord adds a word to the workspace dictionary: the suggested
// word of the finding request.FindingID names, or request.Word. Adding a word
// twice succeeds. Service failures come back as *Error.
func (c *Client) AddDictionaryWord(ctx context.Context, request DictionaryAddRequest) (DictionaryAddResponse, error) {
	var decoded DictionaryAddResponse
	if err := c.write(ctx, dictionaryPath, request, &decoded); err != nil {
		return DictionaryAddResponse{}, err
	}
	if decoded.Word == "" {
		decoded.Word = request.Word
	}
	return decoded, nil
}

// Dictionary lists the workspace dictionary. Service failures come back as
// *Error.
func (c *Client) Dictionary(ctx context.Context) (DictionaryList, error) {
	var decoded DictionaryList
	if err := c.read(ctx, dictionaryPath, &decoded); err != nil {
		return DictionaryList{}, err
	}
	if decoded.Words == nil {
		decoded.Words = []DictionaryWord{}
	}
	return decoded, nil
}

// RemoveDictionaryWord removes word from the workspace dictionary. Removing
// a word the dictionary does not hold succeeds. Service failures come back
// as *Error.
func (c *Client) RemoveDictionaryWord(ctx context.Context, word string) error {
	if !ValidDictionaryWord(word) {
		return fmt.Errorf("%q is not a dictionary word", word)
	}
	path := dictionaryPath + "/" + url.PathEscape(word)
	status, _, payload, err := c.send(ctx, http.MethodDelete, path, nil)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return decodeError(status, payload)
	}
	return nil
}
