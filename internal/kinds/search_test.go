package kinds

import (
	"testing"
)

func TestUserDefinedTypeSchema_SearchDeclaration(t *testing.T) {
	s := UserDefinedTypeSchema{
		ID:    "reading-list",
		Label: "reading list",
		Fields: map[string]UserDefinedField{
			"title":  {Type: "string"},
			"author": {Type: "string"},
		},
		Identity: UserDefinedIdentity{Fields: []string{"title"}},
		Search:   &UserDefinedSearchSpec{Fields: []string{"title", "author"}},
	}
	if err := s.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	k := s.LibraryKind()
	if k.Search == nil {
		t.Fatal("kind missing search spec")
	}
	if k.Search.TitleField != "name" {
		t.Fatalf("title field default = %q", k.Search.TitleField)
	}
	if len(k.Search.Fields) != 2 {
		t.Fatalf("fields = %v", k.Search.Fields)
	}
}

func TestUserDefinedTypeSchema_SearchValidation(t *testing.T) {
	base := func() UserDefinedTypeSchema {
		return UserDefinedTypeSchema{
			ID:    "reading-list",
			Label: "reading list",
			Fields: map[string]UserDefinedField{
				"title": {Type: "string"},
			},
			Identity: UserDefinedIdentity{Fields: []string{"title"}},
		}
	}
	s := base()
	s.Search = &UserDefinedSearchSpec{Fields: []string{"missing"}}
	if err := s.Normalize(); err == nil {
		t.Fatal("unknown search field must be rejected")
	}
	s = base()
	s.Search = &UserDefinedSearchSpec{}
	if err := s.Normalize(); err == nil {
		t.Fatal("empty search fields must be rejected")
	}
	s = base()
	s.Search = &UserDefinedSearchSpec{Fields: []string{"title"}, TitleField: "nope"}
	if err := s.Normalize(); err == nil {
		t.Fatal("unknown title_field must be rejected")
	}
	s = base()
	s.Search = &UserDefinedSearchSpec{Fields: []string{"title", "name"}}
	if err := s.Normalize(); err != nil {
		t.Fatalf("name is always searchable: %v", err)
	}
}
