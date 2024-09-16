---
title: "wf CLI ref: warpforge catalog"
layout: base.njk
eleventyNavigation:
    parent: Warpforge CLI
    order: 40
---


`warpforge catalog` command reference
=====================================

[testmark]:# (docs)
```clidoc
## NAME
warpforge catalog - Subcommands that operate on catalogs

## USAGE
warpforge catalog command [command options] [arguments...]

## COMMANDS
### init
Creates a named catalog in the root workspace

### add
Add an item to the given catalog in the root workspace. Will create a catalog if required.

### release
Add a module to the root workspace catalog as a new release

### ls
List available catalogs in the root workspace and their contents

### show
Show the contents of a module in the root workspace catalog

### bundle
Bundle required catalog items into the local workspace.

### update
Update remote catalogs in the root workspace. Will install the default warpsys catalog.

### ingest-git-tags
Ingest all tags from a git repository into a root workspace catalog entry

### generate-html
Generates HTML output for the root workspace catalog containing information on modules

### mirror
Mirror the contents of a catalog to remote warehouses

### serve
Generates html and serves

### help, h
Shows a list of commands or help for one command

## OPTIONS
#### --name=<VALUE>, -n=<VALUE>
Name of the catalog to operate on

(default: **"default"**)

#### --force, -f
Force operation, even if it causes data to be overwritten.

(default: **false**)

#### --help, -h
show help

```
