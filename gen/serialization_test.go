package gen

import "testing"

func TestParseSerialization(t *testing.T) {
	cases := []struct {
		in      string
		want    Serialization
		wantErr bool
	}{
		{"canonical", SerializationCanonical, false},
		{"verbatim", SerializationVerbatim, false},
		{"", SerializationCanonical, false}, // key absent → default
		{"Verbatim", 0, true},               // exact spelling only, like minify levels
		{"strict", 0, true},
	}
	for _, c := range cases {
		got, err := parseSerialization(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("parseSerialization(%q) err=%v wantErr=%v", c.in, err, c.wantErr)
			continue
		}
		if err == nil && got != c.want {
			t.Errorf("parseSerialization(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestWithSerializationOverridesConfigFile(t *testing.T) {
	base := config{serialization: SerializationVerbatim} // as if loaded from gsx.toml
	var opts config
	for _, o := range []Option{WithSerialization(SerializationCanonical)} {
		o(&opts)
	}
	merged := mergeConfig(base, opts)
	if merged.serialization != SerializationCanonical || !merged.serializationSet {
		t.Fatalf("option must win over config file: got %v set=%v", merged.serialization, merged.serializationSet)
	}
}

func TestMergeConfigKeepsFileSerialization(t *testing.T) {
	base := config{serialization: SerializationVerbatim}
	merged := mergeConfig(base, config{})
	if merged.serialization != SerializationVerbatim {
		t.Fatalf("file-layer serialization lost in merge: %v", merged.serialization)
	}
}
