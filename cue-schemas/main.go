// A module for vendoring, publishing and exporting CUE schemas

package main

import (
	"context"
	"dagger/cue-schemas/internal/dagger"
	_ "embed"
	"fmt"
	"strings"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/cuecontext"
	"cuelang.org/go/encoding/yaml"
	"github.com/Masterminds/semver/v3"
	"github.com/google/go-github/github"
	yamlv3 "gopkg.in/yaml.v3"
)

type CueSchemas struct {
	// +private
	CueVersion string
	// +private
	TimoniVersion string
	// +private
	GolangImage string
}

type VendoredSchema struct {
	// the name of the module
	Name string
	// the module version
	Version string
	// the module directory
	Directory *dagger.Directory
}

type GithubSource struct {
	Tag    string   `yaml:"tag"`
	Ref    string   `yaml:"ref"`
	Owner  string   `yaml:"owner"`
	Repo   string   `yaml:"repo"`
	Files  []string `yaml:"files"`
	Dirs   []string `yaml:"dirs"`
	Assets []string `yaml:"assets"`
}

type KubernetesSource struct {
	Version string `yaml:"version"`
}

type Sources struct {
	Github     []GithubSource     `yaml:"github"`
	Kubernetes []KubernetesSource `yaml:"kubernetes"`
}

//go:embed schema.cue
var schemaFile string

func New(
	// +optional
	// +default="latest"
	// the cue version to use
	cueVersion string,
	// +optional
	// +default="latest"
	// the timoni version to use
	timoniVersion string,
	// +optional
	// +default="golang:latest"
	// the golang image to use
	golangImage string,
) *CueSchemas {
	return &CueSchemas{
		CueVersion:    cueVersion,
		TimoniVersion: timoniVersion,
		GolangImage:   golangImage,
	}
}

// container with the cue and timoni binaries
func (m *CueSchemas) Container() *dagger.Container {
	return dag.Container().
		From(m.GolangImage).
		WithExec([]string{"go", "install", fmt.Sprintf("github.com/stefanprodan/timoni/cmd/timoni@%s", m.TimoniVersion)}).
		WithExec([]string{"go", "install", fmt.Sprintf("cuelang.org/go/cmd/cue@%s", m.CueVersion)})
}

// vendor kubernetes api schemas
func (m *CueSchemas) VendorKubernetes(
	// the kubernetes version to vendor
	version string,
) *VendoredSchema {
	semver := semver.MustParse(version)
	return &VendoredSchema{
		Name:    "k8s.io",
		Version: version,
		Directory: m.Container().
			WithExec([]string{"cue", "mod", "init"}).
			WithExec([]string{"timoni", "mod", "vendor", "k8s", "-v", fmt.Sprintf("%d.%d", semver.Major(), semver.Minor())}).
			Directory("cue.mod/gen/k8s.io"),
	}
}

// vendor timoni schemas for the current version
func (m *CueSchemas) VendorTimoni() *VendoredSchema {
	return &VendoredSchema{
		Name:    "timoni.sh",
		Version: m.TimoniVersion,
		Directory: m.Container().
			WithExec([]string{"timoni", "mod", "init", "derp"}).
			Directory("derp/cue.mod/pkg/timoni.sh"),
	}
}

// vendor kubernetes crd schemas from github
func (m *CueSchemas) VendorGithub(
	ctx context.Context,
	// the desired tag
	tag string,
	// the github ref
	// +optional
	ref string,
	// the github owner
	owner string,
	// the github repo
	repo string,
	// +optional
	// the repo files to vendor
	file []string,
	// +optional
	// the repo directories to vendor
	dir []string,
	// +optional
	// the repo release assets to vendor
	asset []string,
) ([]*VendoredSchema, error) {
	client := github.NewClient(nil)
	if ref == "" {
		ref = tag
	}
	var files []string
	for _, f := range file {
		files = append(files, fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/refs/tags/%s/%s", owner, repo, ref, f))
	}
	for _, d := range dir {
		_, entries, _, err := client.Repositories.GetContents(ctx, owner, repo, d, &github.RepositoryContentGetOptions{Ref: ref})
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if strings.HasSuffix(e.GetName(), ".yml") || strings.HasSuffix(e.GetName(), ".yaml") {
				files = append(files, e.GetDownloadURL())
			}
		}
	}
	for _, a := range asset {
		files = append(files, fmt.Sprintf("https://github.com/%s/%s/releases/download/%s/%s", owner, repo, ref, a))
	}
	ctr := m.Container().
		WithExec([]string{"cue", "mod", "init"})
	for _, f := range files {
		ctr = ctr.WithExec([]string{"timoni", "mod", "vendor", "crds", "-f", f})
	}
	ctr = ctr.WithWorkdir("cue.mod/gen")
	mods, _ := ctr.Directory(".").Entries(ctx)
	var result []*VendoredSchema
	for _, mod := range mods {
		result = append(result, &VendoredSchema{
			Name:      mod,
			Version:   tag,
			Directory: ctr.Directory(mod),
		})
	}
	return result, nil
}

// validate a sources.yaml file
func (m *CueSchemas) Validate(
	ctx context.Context,
	// path to the sources.yaml file
	file *dagger.File,
) error {
	cctx := cuecontext.New()
	schema := cctx.CompileString(schemaFile).LookupPath(cue.ParsePath("#Schema"))
	contents, _ := file.Contents(ctx)
	return yaml.Validate([]byte(contents), schema)
}

// vendor schemas from a sources.yaml file
func (m *CueSchemas) Vendor(
	ctx context.Context,
	// path to the sources.yaml file
	file *dagger.File,
) ([]*VendoredSchema, error) {
	if err := m.Validate(ctx, file); err != nil {
		return nil, err
	}
	contents, err := file.Contents(ctx)
	if err != nil {
		return nil, err
	}
	var sources Sources
	if err = yamlv3.Unmarshal([]byte(contents), &sources); err != nil {
		return nil, err
	}
	result := []*VendoredSchema{m.VendorTimoni()}
	for _, k := range sources.Kubernetes {
		result = append(result, m.VendorKubernetes(k.Version))
	}
	for _, g := range sources.Github {
		r, err := m.VendorGithub(ctx, g.Tag, g.Ref, g.Owner, g.Repo, g.Files, g.Dirs, g.Assets)
		if err != nil {
			return nil, err
		}
		result = append(result, r...)
	}
	return result, nil
}

// publish schemas from a source.yaml file to the central registry
func (m *CueSchemas) Publish(
	ctx context.Context,
	// path to the sources.yaml file
	file *dagger.File,
	// the registry owner
	owner string,
	// the registry repo
	repo string,
	// the registry token
	token *dagger.Secret,
) (string, error) {
	schemas, err := m.Vendor(ctx, file)
	if err != nil {
		return "", err
	}
	ctr := m.Container().
		WithSecretVariable("CUE_TOKEN", token).
		WithExec([]string{"sh", "-c", "cue login --token $CUE_TOKEN"})
	var result string
	for _, s := range schemas {
		semver := semver.MustParse(s.Version)
		stdout, err := ctr.WithDirectory(s.Name, s.Directory).
			WithWorkdir(s.Name).
			WithExec([]string{"cue", "mod", "init", fmt.Sprintf("github.com/%s/%s/%s@v%d", owner, repo, s.Name, semver.Major()), "--source=self"}).
			WithExec([]string{"sh", "-c", fmt.Sprintf("find . -type f -exec sed -i 's/\"%s/\"github.com\\/%s\\/%s\\/%s/g' {} +", s.Name, owner, repo, s.Name)}).
			WithExec([]string{"cue", "mod", "tidy"}).
			WithExec([]string{"cue", "mod", "publish", s.Version, "--ignore", "--json"}).
			Stdout(ctx)
		result += stdout
		if err != nil {
			return result, err
		}
	}
	return result, nil
}

// export kubernetes crd schemas from github
func (m *CueSchemas) ExportGithub(
	ctx context.Context,
	// the github ref
	ref string,
	// the github owner
	owner string,
	// the github repo
	repo string,
	// +optional
	// the repo files to vendor
	file []string,
	// +optional
	// the repo directories to vendor
	dir []string,
	// +optional
	// the repo release assets to vendor
	asset []string,
) (*dagger.File, error) {
	client := github.NewClient(nil)
	var files []string
	for _, f := range file {
		files = append(files, fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/refs/tags/%s/%s", owner, repo, ref, f))
	}
	for _, d := range dir {
		_, entries, _, err := client.Repositories.GetContents(ctx, owner, repo, d, &github.RepositoryContentGetOptions{Ref: ref})
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if strings.HasSuffix(e.GetName(), ".yml") || strings.HasSuffix(e.GetName(), ".yaml") {
				files = append(files, e.GetDownloadURL())
			}
		}
	}
	for _, a := range asset {
		files = append(files, fmt.Sprintf("https://github.com/%s/%s/releases/download/%s/%s", owner, repo, ref, a))
	}
	ctr := m.Container().
		WithWorkdir("/tmp/gen")
	for _, f := range files {
		ctr = ctr.WithExec([]string{"wget", f})
	}
	ctr = ctr.WithExec([]string{"cue", "import", "-fl", "strings.ToLower(kind)", "-l", "strings.ToLower(metadata.name)", "-p", "crds"}).
		WithExec([]string{"cue", "export", "-e", "customresourcedefinition", "-o", "crds.cue"})
	return ctr.File("crds.cue"), nil
}

// export kubernetes crds from a sources.yaml file
func (m *CueSchemas) Export(
	ctx context.Context,
	// path to the sources.yaml file
	file *dagger.File,
) (*dagger.Directory, error) {
	if err := m.Validate(ctx, file); err != nil {
		return nil, err
	}
	contents, _ := file.Contents(ctx)
	var sources Sources
	if err := yamlv3.Unmarshal([]byte(contents), &sources); err != nil {
		return nil, err
	}
	ctr := dag.Container()
	for _, s := range sources.Github {
		if s.Ref == "" {
			s.Ref = s.Tag
		}
		crds, err := m.ExportGithub(ctx, s.Ref, s.Owner, s.Repo, s.Files, s.Dirs, s.Assets)
		if err != nil {
			return nil, err
		}
		ctr = ctr.WithFile(s.Owner+"-"+s.Repo+".cue", crds)
	}
	return ctr.Directory("."), nil
}
