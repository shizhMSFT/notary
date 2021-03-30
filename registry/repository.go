package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"

	artifactspecs "github.com/aviral26/artifacts/specs-go/v2"
	"github.com/notaryproject/notary/v2/util"
	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	oci "github.com/opencontainers/image-spec/specs-go/v1"
)

const maxReadLimit = 4 * 1024 * 1024

type repository struct {
	tr   http.RoundTripper
	base string
	name string
}

func (r *repository) Lookup(ctx context.Context, manifestDigest digest.Digest) ([]digest.Digest, error) {
	url, err := url.Parse(fmt.Sprintf("%s/_ext/oci-artifacts/v1/%s/manifests/%s/links", r.base, r.name, manifestDigest.String()))
	if err != nil {
		return nil, err
	}
	q := url.Query()
	q.Add("artifact-type", artifactspecs.ArtifactTypeNotaryV2)
	url.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := r.tr.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to lookup signatures: %s", resp.Status)
	}

	result := struct {
		Links []Artifact `json:"links"`
	}{}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxReadLimit)).Decode(&result); err != nil {
		return nil, err
	}
	digests := make([]digest.Digest, 0, len(result.Links))
	for _, artifact := range result.Links {
		digests = append(digests, artifact.Config.Digest)
	}
	return digests, nil
}

func (r *repository) Get(ctx context.Context, signatureDigest digest.Digest) ([]byte, error) {
	return r.getBlob(ctx, signatureDigest)
}

func (r *repository) Put(ctx context.Context, signature []byte) (oci.Descriptor, error) {
	desc := util.DescriptorFromBytes(signature)
	desc.MediaType = MediaTypeNotaryConfig
	return desc, r.putBlob(ctx, signature, desc.Digest)
}

func (r *repository) Link(ctx context.Context, manifest, signature oci.Descriptor) (oci.Descriptor, error) {
	artifact := Artifact{
		Versioned: specs.Versioned{
			SchemaVersion: 2,
		},
		MediaType:    artifactspecs.MediaTypeArtifact,
		ArtifactType: artifactspecs.ArtifactTypeNotaryV2,
		Config:       signature,
		Manifests: []oci.Descriptor{
			manifest,
		},
	}
	artifactJSON, err := json.Marshal(artifact)
	if err != nil {
		return oci.Descriptor{}, err
	}
	desc := util.DescriptorFromBytes(artifactJSON)
	return desc, r.putManifest(ctx, artifactJSON, desc.Digest)
}

func (r *repository) getBlob(ctx context.Context, digest digest.Digest) ([]byte, error) {
	url := fmt.Sprintf("%s/%s/blobs/%s", r.base, r.name, digest.String())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := r.tr.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return ioutil.ReadAll(io.LimitReader(resp.Body, maxReadLimit))
	}
	if resp.StatusCode != http.StatusTemporaryRedirect {
		return nil, fmt.Errorf("failed to get blob: %s", resp.Status)
	}
	resp.Body.Close()

	location, err := resp.Location()
	if err != nil {
		return nil, err
	}
	req, err = http.NewRequestWithContext(ctx, http.MethodGet, location.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err = r.tr.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to get blob: %s", resp.Status)
	}
	return ioutil.ReadAll(io.LimitReader(resp.Body, maxReadLimit))
}

func (r *repository) putBlob(ctx context.Context, blob []byte, digest digest.Digest) error {
	url := fmt.Sprintf("%s/%s/blobs/uploads/", r.base, r.name)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return err
	}
	resp, err := r.tr.RoundTrip(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("failed to init upload: %s", resp.Status)
	}

	url = resp.Header.Get("Location")
	if url == "" {
		return http.ErrNoLocation
	}

	req, err = http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(blob))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	q := req.URL.Query()
	q.Add("digest", digest.String())
	req.URL.RawQuery = q.Encode()
	resp, err = r.tr.RoundTrip(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("failed to upload: %s", resp.Status)
	}
	return nil
}

func (r *repository) putManifest(ctx context.Context, blob []byte, digest digest.Digest) error {
	url := fmt.Sprintf("%s/%s/manifests/%s", r.base, r.name, digest.String())
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(blob))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", artifactspecs.MediaTypeArtifact)
	resp, err := r.tr.RoundTrip(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("failed to put manifest: %s", resp.Status)
	}
	return nil
}