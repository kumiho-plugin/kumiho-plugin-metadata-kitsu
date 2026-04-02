package plugin

import (
	"fmt"
	"testing"
)

func TestBuildFetchCharactersSortsAndMapsCharacters(t *testing.T) {
	payload := fetchResponse{
		Included: []includedResource{
			{
				ID:   "rel-2",
				Type: "mediaCharacters",
				Attributes: includedAttributes{
					Role: "supporting",
				},
				Relationships: includedRelationships{
					Character: relatedResource{Data: resourceIdentifier{ID: "char-2", Type: "characters"}},
				},
			},
			{
				ID:   "rel-1",
				Type: "mediaCharacters",
				Attributes: includedAttributes{
					Role: "main",
				},
				Relationships: includedRelationships{
					Character: relatedResource{Data: resourceIdentifier{ID: "char-1", Type: "characters"}},
				},
			},
			{
				ID:   "char-1",
				Type: "characters",
				Attributes: includedAttributes{
					CanonicalName: "Edward Elric",
					Image: imageSet{
						Original: "https://example.com/edward.jpg",
					},
				},
			},
			{
				ID:   "char-2",
				Type: "characters",
				Attributes: includedAttributes{
					Name: "Alphonse Elric",
				},
			},
		},
	}

	characters := buildFetchCharacters(payload)
	if len(characters) != 2 {
		t.Fatalf("len(characters) = %d, want 2", len(characters))
	}
	if characters[0].Name != "Edward Elric" {
		t.Fatalf("characters[0].Name = %q", characters[0].Name)
	}
	if characters[0].Role != "main" {
		t.Fatalf("characters[0].Role = %q", characters[0].Role)
	}
	if characters[0].Image == nil || characters[0].Image.URL != "https://example.com/edward.jpg" {
		t.Fatalf("characters[0].Image = %#v", characters[0].Image)
	}
	if characters[0].Identifiers["kitsu_media_character_id"] != "rel-1" {
		t.Fatalf("media character id = %q", characters[0].Identifiers["kitsu_media_character_id"])
	}
	if characters[1].Name != "Alphonse Elric" {
		t.Fatalf("characters[1].Name = %q", characters[1].Name)
	}
	if characters[1].Role != "supporting" {
		t.Fatalf("characters[1].Role = %q", characters[1].Role)
	}
}

func TestBuildFetchCharactersSortsMainCharactersAlphabetically(t *testing.T) {
	payload := fetchResponse{
		Included: []includedResource{
			{
				ID:   "rel-2",
				Type: "mediaCharacters",
				Attributes: includedAttributes{
					Role: "main",
				},
				Relationships: includedRelationships{
					Character: relatedResource{Data: resourceIdentifier{ID: "char-2", Type: "characters"}},
				},
			},
			{
				ID:   "rel-1",
				Type: "mediaCharacters",
				Attributes: includedAttributes{
					Role: " main ",
				},
				Relationships: includedRelationships{
					Character: relatedResource{Data: resourceIdentifier{ID: "char-1", Type: "characters"}},
				},
			},
			{
				ID:   "rel-3",
				Type: "mediaCharacters",
				Attributes: includedAttributes{
					Role: "supporting",
				},
				Relationships: includedRelationships{
					Character: relatedResource{Data: resourceIdentifier{ID: "char-3", Type: "characters"}},
				},
			},
			{
				ID:   "char-1",
				Type: "characters",
				Attributes: includedAttributes{
					Name: "Alpha",
				},
			},
			{
				ID:   "char-2",
				Type: "characters",
				Attributes: includedAttributes{
					Name: "Zeta",
				},
			},
			{
				ID:   "char-3",
				Type: "characters",
				Attributes: includedAttributes{
					Name: "Beta",
				},
			},
		},
	}

	characters := buildFetchCharacters(payload)
	if len(characters) != 3 {
		t.Fatalf("len(characters) = %d, want 3", len(characters))
	}

	got := []string{characters[0].Name, characters[1].Name, characters[2].Name}
	want := []string{"Alpha", "Zeta", "Beta"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("characters order = %#v, want %#v", got, want)
		}
	}
}

func TestBuildFetchCharactersLimitsToTopTwenty(t *testing.T) {
	payload := fetchResponse{
		Included: make([]includedResource, 0, 44),
	}

	for i := 0; i < 22; i++ {
		id := fmt.Sprintf("%02d", i)
		payload.Included = append(payload.Included,
			includedResource{
				ID:   "rel-" + id,
				Type: "mediaCharacters",
				Attributes: includedAttributes{
					Role: "supporting",
				},
				Relationships: includedRelationships{
					Character: relatedResource{Data: resourceIdentifier{ID: "char-" + id, Type: "characters"}},
				},
			},
			includedResource{
				ID:   "char-" + id,
				Type: "characters",
				Attributes: includedAttributes{
					Name: fmt.Sprintf("Character %02d", i),
				},
			},
		)
	}

	characters := buildFetchCharacters(payload)
	if len(characters) != maxFetchedCharacters {
		t.Fatalf("len(characters) = %d, want %d", len(characters), maxFetchedCharacters)
	}
	if characters[0].Name != "Character 00" {
		t.Fatalf("characters[0].Name = %q", characters[0].Name)
	}
	if characters[len(characters)-1].Name != "Character 19" {
		t.Fatalf("characters[last].Name = %q", characters[len(characters)-1].Name)
	}
}

func TestBuildFetchCharactersSkipsMissingCharacterJoin(t *testing.T) {
	payload := fetchResponse{
		Included: []includedResource{
			{
				ID:   "rel-1",
				Type: "mediaCharacters",
				Relationships: includedRelationships{
					Character: relatedResource{Data: resourceIdentifier{ID: "char-404", Type: "characters"}},
				},
			},
		},
	}

	characters := buildFetchCharacters(payload)
	if len(characters) != 0 {
		t.Fatalf("len(characters) = %d, want 0", len(characters))
	}
}
