# Ditto Repo Dedupe

This repository contains a proof-of-concept for a tool that mirrors debian repositories into a single directory, deduplicating files using a content-addressable storage approach.

The tool leverages [ditto-repo](https://github.com/canonical/ditto-repo) as the engine for repository mirroring. Because ditto-repo does not support content-addressable storage, this tool implements a layer on top of it to achieve deduplication. This is done via an implementation of ditto-repo's `FileSystem` interface, which allows us to intercept file operations and store files in a content-addressable manner.

## Usage

To use the tool, you can run the `main.go` file with the appropriate arguments. For example:

```bash
go run cmd/main.go --source <source-repo-url> --dist <dist-directory> --arch <architecture> --destination <destination-directory> [--store <store-directory>]
```

This will mirror the specified source repository into the destination directory, while deduplicating files based on their content. The `--source` argument specifies the URL of the source repository, `--dist` specifies the distribution to mirror, `--arch` specifies the architecture, and `--destination` specifies the directory where the mirrored repository will be stored.

The `--store` argument is optional and specifies the directory holding the shared content-addressable store. Because the store lives _above_ the mirror destination rather than inside it, several mirrors can point at the same `--store` to deduplicate content across all of them. When omitted, it defaults to a `content-addressable` directory alongside the destination.

## Implementation Details

### Content-Addressable Storage

The tool implements a content-addressable storage mechanism by hashing the contents of files and storing them in a directory structure based on their hash values. This allows for efficient deduplication, as identical files will have the same hash and can be stored only once.

An example resulting directory structure might look like this:

```
mirrors/
├── content-addressable/
│   ├── 21/
│   │   └── c1/
│   │       └── 47cf0bcf218ce9cec53efcb93bd6 (app_1.0.0_amd64.deb)
│   └── a1/
│       ├── b2/
│       |   └── c3d4e5f67890abcdef1234567890 (Packages)
│       └── c3/
│           └── c3d4e5f67890abcdef1234567890 (Release)
├── repo-a/
│   ├── dists/
│   │   └── focal/
│   │       └── main/
│   │           └── binary-amd64/
│   │               ├── Packages
│   │               └── Release
│   └── pool/
│       └── main/
│           └── a/
│               └── app_1.0.0_amd64.deb
└── repo-b/
    ├── dists/
    └── pool/
```

The `content-addressable` directory sits above the individual mirror roots (`repo-a`, `repo-b`) so they can share it. It contains the actual files stored based on their hash values, while the `dists` and `pool` directories of each mirror contain symlinks to these files.

### Download efficiency

When downloading files from the source repository, the tool first checks if a file with the same content hash already exists in the content-addressable storage. If it does, it creates a symlink to the existing file instead of downloading it again. This can significantly reduce the amount of data that needs to be downloaded, especially when mirroring multiple repositories that may contain identical files.

### Implications

This approach allows for significant storage savings when mirroring multiple repositories that may contain identical files. By storing files in a content-addressable manner, we can avoid storing duplicate files and instead reference them via symlinks or even hardlinks, resulting in a more efficient use of storage space.

A more sophisticated version of this tool could also implement garbage collection to remove unreferenced files from the content-addressable storage, further optimizing storage usage over time.

An _even more_ sophisticated version could leverage some kind of database to track file references and manage the content-addressable storage more efficiently, especially in scenarios with a large number of files and repositories, but we'll leave that as a future if-need-becomes-apparent enhancement.