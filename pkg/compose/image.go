package compose

import (
	"bytes"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// SetImageTag rewrites every service's image tag to tag (e.g. "pkg", "1.5.5").
// A blank tag is a no-op. Used at install time so a catalog app can be deployed
// on a chosen variant/version instead of the manifest's default.
func SetImageTag(composeYAML, tag string) (string, error) {
	if tag == "" {
		return composeYAML, nil
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(composeYAML), &doc); err != nil {
		return "", fmt.Errorf("parse compose: %w", err)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return "", fmt.Errorf("compose is not a YAML mapping")
	}
	services := mapGet(doc.Content[0], "services")
	if services == nil || services.Kind != yaml.MappingNode {
		return "", fmt.Errorf("compose has no services")
	}
	for i := 1; i < len(services.Content); i += 2 {
		svc := services.Content[i]
		if svc.Kind != yaml.MappingNode {
			continue
		}
		if img := mapGet(svc, "image"); img != nil && img.Kind == yaml.ScalarNode {
			img.Value = replaceTag(img.Value, tag)
			img.Style = 0
		}
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc.Content[0]); err != nil {
		return "", err
	}
	enc.Close()
	return buf.String(), nil
}

// ServiceImages returns every service's image ref, in service order. Used for
// update detection, which must consider all of a stack's images (Update pulls
// them all), not just the first.
func ServiceImages(composeYAML string) ([]string, error) {
	svcs, err := ServiceImageList(composeYAML)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(svcs))
	for i, si := range svcs {
		out[i] = si.Image
	}
	return out, nil
}

// ServiceImage is one service and the image it names.
type ServiceImage struct {
	Service string
	Image   string
}

// ServiceImageList is ServiceImages with the service each image belongs to --
// what an update check needs to say WHICH part of a stack is behind. Services
// without an image (build-only) are left out.
func ServiceImageList(composeYAML string) ([]ServiceImage, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(composeYAML), &doc); err != nil {
		return nil, fmt.Errorf("parse compose: %w", err)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, nil
	}
	services := mapGet(doc.Content[0], "services")
	if services == nil || services.Kind != yaml.MappingNode {
		return nil, nil
	}
	var out []ServiceImage
	for i := 1; i < len(services.Content); i += 2 {
		svc := services.Content[i]
		if svc.Kind != yaml.MappingNode {
			continue
		}
		if img := mapGet(svc, "image"); img != nil && img.Kind == yaml.ScalarNode && img.Value != "" {
			out = append(out, ServiceImage{Service: services.Content[i-1].Value, Image: img.Value})
		}
	}
	return out, nil
}

// eachServiceImage walks the compose and calls fn on every service's image
// scalar node, replacing it with fn's return value. Returns the re-encoded YAML.
func eachServiceImage(composeYAML string, fn func(image string) (string, error)) (string, error) {
	return eachNamedImage(composeYAML, func(_, image string) (string, error) { return fn(image) })
}

// SetServiceImage replaces one service's image, leaving every other service
// as it is -- what a per-service rollback or unpin needs, where SetImageTag
// would retag a whole multi-image stack.
func SetServiceImage(composeYAML, service, image string) (string, error) {
	found := false
	out, err := eachNamedImage(composeYAML, func(name, cur string) (string, error) {
		if name != service {
			return cur, nil
		}
		found = true
		return image, nil
	})
	if err == nil && !found {
		return "", fmt.Errorf("no service %q with an image", service)
	}
	return out, err
}

// SetServiceTag moves one service to another tag of its own image (dropping
// any @digest), leaving the rest alone: the per-service form of SetImageTag,
// which would put a multi-image stack's database on the app's version.
func SetServiceTag(composeYAML, service, tag string) (string, error) {
	found := false
	out, err := eachNamedImage(composeYAML, func(name, cur string) (string, error) {
		if name != service {
			return cur, nil
		}
		found = true
		return replaceTag(cur, tag), nil
	})
	if err == nil && !found {
		return "", fmt.Errorf("no service %q with an image", service)
	}
	return out, err
}

// eachNamedImage is eachServiceImage with the service's name.
func eachNamedImage(composeYAML string, fn func(service, image string) (string, error)) (string, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(composeYAML), &doc); err != nil {
		return "", fmt.Errorf("parse compose: %w", err)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return "", fmt.Errorf("compose is not a YAML mapping")
	}
	services := mapGet(doc.Content[0], "services")
	if services == nil || services.Kind != yaml.MappingNode {
		return "", fmt.Errorf("compose has no services")
	}
	for i := 1; i < len(services.Content); i += 2 {
		svc := services.Content[i]
		if svc.Kind != yaml.MappingNode {
			continue
		}
		img := mapGet(svc, "image")
		if img == nil || img.Kind != yaml.ScalarNode {
			continue
		}
		nv, err := fn(services.Content[i-1].Value, img.Value)
		if err != nil {
			return "", err
		}
		if nv != img.Value {
			img.Value = nv
			img.Style = 0
		}
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc.Content[0]); err != nil {
		return "", err
	}
	enc.Close()
	return buf.String(), nil
}

// PinImageDigests pins every service image to an exact digest, resolved per
// service by resolve (given the current "repo:tag" ref). The result keeps the
// human-readable tag: "repo:tag" -> "repo:tag@sha256:...". Any pre-existing
// digest is re-resolved from the live tag, so re-pinning refreshes it.
func PinImageDigests(composeYAML string, resolve func(ref string) (string, error)) (string, error) {
	return eachServiceImage(composeYAML, func(image string) (string, error) {
		ref := image
		if at := strings.LastIndex(ref, "@"); at >= 0 {
			ref = ref[:at] // strip any existing digest; re-resolve from the tag
		}
		digest, err := resolve(ref)
		if err != nil {
			return "", err
		}
		return ref + "@" + digest, nil
	})
}

// replaceTag swaps the tag on an image reference, preserving the registry/repo
// (and dropping any existing tag or @digest). e.g.
// "ghcr.io/daemonless/radarr:latest" + "pkg" -> "ghcr.io/daemonless/radarr:pkg".
func replaceTag(image, tag string) string {
	ref := image
	if at := strings.LastIndex(ref, "@"); at >= 0 {
		ref = ref[:at]
	}
	slash := strings.LastIndex(ref, "/")
	if colon := strings.LastIndex(ref, ":"); colon > slash {
		ref = ref[:colon]
	}
	return ref + ":" + tag
}

// ImageTag is the tag of an image reference, or "" when it names none.
//
// It parses the same way replaceTag writes: a digest is stripped first, and a
// colon only introduces a tag when it comes after the last slash -- otherwise
// the colon in "registry:5000/app" reads as one.
func ImageTag(image string) string {
	ref := image
	if at := strings.LastIndex(ref, "@"); at >= 0 {
		ref = ref[:at]
	}
	slash := strings.LastIndex(ref, "/")
	if colon := strings.LastIndex(ref, ":"); colon > slash {
		return ref[colon+1:]
	}
	return ""
}
