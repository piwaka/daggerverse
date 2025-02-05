# cue-schemas

A module for vendoring, publishing and exporting CUE schemas.
See [sources.example.yaml](./sources.example.yaml) for an example configuration.

## Publish to central registry

```bash
# publish 
dagger -m github.com/piwaka/daggerverse/cue-schemas call publish --file ./sources.yaml --owner piwaka --repo cue-schemas --token "env:CUE_TOKEN"
```

## Export CRDs

```bash
# export
dagger -m github.com/piwaka/daggerverse/cue-schemas call export --file ./sources.yaml export --path ./crds
```
