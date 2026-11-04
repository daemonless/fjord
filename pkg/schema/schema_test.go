package schema_test

import (
	"testing"

	"github.com/daemonless/fjord/pkg/schema"
)

var validManifest = []byte(`{
  "name": "immich",
  "services": {
    "immich-server": {
      "image": "ghcr.io/daemonless/immich-server:${IMMICH_VERSION:-latest}"
    }
  },
  "x-fjord": {
    "version": "0.1",
    "info": {
      "id": "immich",
      "name": "Immich",
      "description": "High performance self-hosted photo and video management solution.",
      "category": "Photos & Media",
      "class": "stack",
      "icon": "icons/immich.svg",
      "web_port": "${WEB_PORT}",
      "own_origin": true,
      "name_env": {
        "IMMICH_PUBLIC_URL": "https://{name}"
      }
    },
    "networking": {
      "immich-server": "default",
      "*": "private"
    },
    "hostnames": {
      "database": "DB_HOSTNAME"
    },
    "login": {
      "user": "admin",
      "password": "${ADMIN_PASSWORD}",
      "note": "First-run setup"
    },
    "provides": {
      "names": {
        "port": 8443,
        "key": "${RP_PAIRING_KEY}",
        "setup_token": "${RP_SETUP_TOKEN}",
        "client_url": "${RP_PAIRING_URL}",
        "client_id": "${RP_PAIRING_ID}",
        "client_name": "${RP_PAIRING_NAME}"
      }
    },
    "choices": [
      {
        "id": "machine_learning",
        "kind": "part",
        "label": "Machine learning",
        "default": "on",
        "options": [
          { "id": "on" },
          {
            "id": "off",
            "env": { "IMMICH_ML_ENABLED": "false" },
            "drop": ["immich-machine-learning"]
          }
        ]
      }
    ],
    "variables": [
      {
        "name": "WEB_PORT",
        "type": "port",
        "default": "2283"
      },
      {
        "name": "IMMICH_VERSION",
        "type": "image_tag",
        "default": "latest",
        "image": "ghcr.io/daemonless/immich-server"
      },
      {
        "name": "PUBLIC_URL",
        "type": "public_url",
        "default": "",
        "optional": true
      },
      {
        "name": "GITEA__database__HOST",
        "type": "string",
        "default": "postgres"
      },
      {
        "name": "CONFIG_DATA",
        "label": "Configuration directory",
        "type": "zfs_dataset",
        "default": "config",
        "host_permissions": {
          "uid": 1000,
          "gid": 1000,
          "mode": "755"
        }
      }
    ]
  }
}`)

var invalidManifestMissingInfo = []byte(`{
  "x-fjord": {
    "version": "0.1"
  }
}`)

var invalidManifestUnknownType = []byte(`{
  "x-fjord": {
    "version": "0.1",
    "info": {
      "id": "radarr",
      "name": "Radarr",
      "description": "Automated movie collection manager.",
      "category": "Media Management",
      "class": "service"
    },
    "variables": [
      {
        "name": "WEB_PORT",
        "type": "not_a_type"
      }
    ]
  }
}`)

var validCatalog = []byte(`{
  "catalog_name": "Daemonless",
  "fjord_version": "0.1",
  "maintainer": "https://daemonless.io",
  "icon": "icons/catalog.svg",
  "os": ["freebsd"],
  "generated": "2026-11-04T19:00:00Z",
  "apps": [
    {
      "id": "immich",
      "name": "Immich",
      "category": "Photos & Media",
      "class": "stack",
      "icon": "icons/immich.svg",
      "image": "ghcr.io/daemonless/immich-server",
      "manifest_url": "manifests/immich.yaml",
      "version": "v3.3.5",
      "architectures": ["amd64"],
      "variants": [],
      "parts": [
        {
          "id": "immich-server",
          "name": "Immich Server",
          "image": "ghcr.io/daemonless/immich-server"
        }
      ]
    },
    {
      "id": "radarr",
      "name": "Radarr",
      "category": "Media Management",
      "class": "service",
      "icon": "icons/radarr.svg",
      "manifest_url": "manifests/radarr.yaml",
      "version": "6.4.4.10685",
      "architectures": ["amd64"],
      "variants": [
        {
          "id": "latest",
          "label": "Latest",
          "default": true,
          "image": "ghcr.io/daemonless/radarr:latest",
          "version": "6.4.4.10685"
        }
      ]
    }
  ]
}`)

var invalidCatalog = []byte(`{
  "catalog_name": "Daemonless Apps",
  "fjord_version": "0.1"
}`)

func TestValidateManifest(t *testing.T) {
	if err := schema.ValidateManifest(validManifest); err != nil {
		t.Fatalf("expected valid manifest to pass, got error: %v", err)
	}

	if err := schema.ValidateManifest(invalidManifestMissingInfo); err == nil {
		t.Fatalf("expected invalid manifest to fail, but it passed")
	}

	if err := schema.ValidateManifest(invalidManifestUnknownType); err == nil {
		t.Fatalf("expected unknown variable type to fail, but it passed")
	}
}

func TestValidateCatalog(t *testing.T) {
	if err := schema.ValidateCatalog(validCatalog); err != nil {
		t.Fatalf("expected valid catalog to pass, got error: %v", err)
	}

	if err := schema.ValidateCatalog(invalidCatalog); err == nil {
		t.Fatalf("expected invalid catalog to fail, but it passed")
	}
}
