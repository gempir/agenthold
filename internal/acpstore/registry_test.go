package acpstore

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gempir/agenthold/internal/store/db"
)

const sampleRegistry = `{
  "version": "1.0.0",
  "agents": [
    {
      "id": "goose",
      "name": "Goose",
      "version": "1.2.0",
      "description": "Open source coding agent by Block",
      "authors": ["Block"],
      "license": "Apache-2.0",
      "website": "https://block.github.io/goose",
      "distribution": {
        "binary": {
          "linux-x86_64": {"archive": "https://example.com/goose-x64.tar.gz", "cmd": "./goose", "args": ["acp"]},
          "linux-aarch64": {"archive": "https://example.com/goose-arm64.tar.gz", "cmd": "goose", "args": ["acp"]}
        }
      }
    },
    {
      "id": "mistral-vibe",
      "name": "Mistral Vibe",
      "version": "3.1.0",
      "description": "Mistral CLI coding agent",
      "authors": ["Mistral AI"],
      "distribution": {
        "uvx": {"package": "mistral-vibe==3.1.0", "args": ["acp"]}
      }
    },
    {
      "id": "bad-agent",
      "name": "Bad Agent",
      "version": "1.0.0",
      "description": "Should be dropped because http:// and unpinned npx",
      "distribution": {
        "binary": {
          "linux-x86_64": {"archive": "http://insecure.example.com/x.tar.gz", "cmd": "../escape"}
        },
        "npx": {"package": "unpinned-pkg"}
      }
    },
    {
      "id": "codex-acp",
      "name": "Codex",
      "version": "2.6.0",
      "description": "Built-in harness duplicate",
      "distribution": {
        "npx": {"package": "@agentclientprotocol/codex-acp@2.6.0"}
      }
    }
  ]
}`

func TestDecodeIndexValidatesAndDropsUnsafeEntries(t *testing.T) {
	idx, err := DecodeIndex([]byte(sampleRegistry))
	if err != nil {
		t.Fatal(err)
	}
	if idx.Version != "1.0.0" {
		t.Fatalf("version = %q", idx.Version)
	}
	if len(idx.Agents) != 3 {
		t.Fatalf("expected 3 valid agents, got %d: %+v", len(idx.Agents), idx.Agents)
	}
	for _, a := range idx.Agents {
		if a.ID == "bad-agent" {
			t.Fatal("bad-agent should have been filtered out")
		}
	}
}

func TestCatalogSearchAndRank(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(sampleRegistry))
	}))
	defer srv.Close()

	cat := NewCatalog(t.TempDir())
	cat.URL = srv.URL

	hits, ver, err := cat.Search(context.Background(), "goose", 10, "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if ver != "1.0.0" || len(hits) != 1 || hits[0].Agent.ID != "goose" {
		t.Fatalf("hits = %+v ver=%q", hits, ver)
	}
	if len(hits[0].Distributions) != 1 || hits[0].Distributions[0] != "binary" {
		t.Fatalf("dists = %v", hits[0].Distributions)
	}

	hits, _, err = cat.Search(context.Background(), "codex", 10, "amd64")
	if err != nil || len(hits) != 1 || hits[0].BuiltinID != "codex" {
		t.Fatalf("builtin tag: %+v err=%v", hits, err)
	}
}

func TestAdapterFromRow(t *testing.T) {
	row := db.CustomHarness{
		ID:               "loc_dsh",
		Kind:             "local",
		Name:             "DeepSeek Harness",
		Command:          "dsh",
		ArgsJson:         `["--profile","acp"]`,
		EnvJson:          `{"DSH_MODE":"fast"}`,
		CustomModelsJson: `["deepseek-reasoner"]`,
	}
	ad := AdapterFromRow(row)
	if ad.ID != "loc_dsh" || ad.Source != "local" || len(ad.Command) != 3 || ad.Command[0] != "dsh" || ad.Command[2] != "acp" {
		t.Fatalf("adapter = %+v", ad)
	}
	if ad.Environment(struct{ Model string }{})["DSH_MODE"] != "fast" {
		t.Fatalf("env = %v", ad.Environment(struct{ Model string }{}))
	}
	if len(ad.FallbackModels) != 1 || ad.FallbackModels[0].ID != "deepseek-reasoner" {
		t.Fatalf("fallbacks = %+v", ad.FallbackModels)
	}
}
