package main

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/urfave/cli/v2"
	"go.opentelemetry.io/otel"

	"github.com/warpfork/warpforge/pkg/dab"
	"github.com/warpfork/warpforge/pkg/logging"
	"github.com/warpfork/warpforge/pkg/plotexec"
	"github.com/warpfork/warpforge/wfapi"
)

const defaultCatalogUrl = "https://github.com/warpsys/mincatalog.git"

var catalogCmdDef = cli.Command{
	Name:  "catalog",
	Usage: "Subcommands that operate on catalogs",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:        "name",
			Aliases:     []string{"n"},
			Usage:       "Name of the catalog to operate on",
			DefaultText: "default",
		},
		&cli.BoolFlag{
			Name:    "force",
			Aliases: []string{"f"},
			Usage:   "Force operation, even if it causes data to be overwritten.",
		},
	},
	Subcommands: []*cli.Command{
		{
			Name:   "init",
			Usage:  "Initialize a catalog in the current directory",
			Action: cmdCatalogInit,
		},
		{
			Name:   "add",
			Usage:  "Add an item to the catalog",
			Action: cmdCatalogAdd,
		},
		{
			Name:   "release",
			Usage:  "Add a module to the catalog as a new release",
			Action: cmdCatalogRelease,
		},
		{
			Name:   "ls",
			Usage:  "List available catalogs",
			Action: cmdCatalogLs,
		},
		{
			Name:   "bundle",
			Usage:  "Bundle required catalog items into this project's catalog",
			Action: cmdCatalogBundle,
		},
		{
			Name:   "update",
			Usage:  "Update remote catalogs",
			Action: cmdCatalogUpdate,
		},
		{
			Name:   "ingest-git-tags",
			Usage:  "Ingest all tags from a git repository into a catalog entry",
			Action: cmdIngestGitTags,
		},
	},
}

func scanWareId(packType wfapi.Packtype, addr wfapi.WarehouseAddr) (wfapi.WareID, error) {
	result := wfapi.WareID{}
	rioPath, err := binPath("rio")
	if err != nil {
		return result, fmt.Errorf("failed to get path to rio")
	}
	rioScan := exec.Command(
		rioPath, "scan", "--source="+string(addr), string(packType),
	)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	rioScan.Stdout = &stdout
	rioScan.Stderr = &stderr
	err = rioScan.Run()
	if err != nil {
		return result, fmt.Errorf("failed to run rio scan command: %s\n%s", err, stderr.String())
	}
	wareIdStr := strings.TrimSpace(stdout.String())
	hash := strings.Split(wareIdStr, ":")[1]
	result = wfapi.WareID{
		Packtype: wfapi.Packtype(packType),
		Hash:     hash,
	}

	return result, nil
}

func cmdCatalogInit(c *cli.Context) error {
	if c.Args().Len() < 1 {
		return fmt.Errorf("no catalog name provided")
	}
	catalogName := c.Args().First()
	fsys := os.DirFS("/")

	// open the workspace set and get the catalog path
	wsSet, err := openWorkspaceSet(fsys)
	if err != nil {
		return err
	}
	catalogPath := filepath.Join("/", wsSet.Root.CatalogPath(&catalogName))

	// check if the catalog directory exists
	_, err = os.Stat(catalogPath)
	if os.IsNotExist(err) {
		// catalog does not exist, create the dir
		err = os.MkdirAll(catalogPath, 0755)
	} else {
		// catalog already exists
		return fmt.Errorf("catalog %q already exists (path: %q)", catalogName, catalogPath)
	}

	if err != nil {
		// stat or mkdir failed
		return fmt.Errorf("failed to create catalog: %s", err)
	}

	return nil
}

func cmdCatalogAdd(c *cli.Context) error {
	if c.Args().Len() < 3 {
		return fmt.Errorf("invalid input. usage: warpforge catalog add [pack type] [catalog ref] [url] [ref]")
	}

	catalogName := c.String("name")

	packType := c.Args().Get(0)
	catalogRefStr := c.Args().Get(1)
	url := c.Args().Get(2)

	fsys := os.DirFS("/")

	// open the workspace set
	wsSet, err := openWorkspaceSet(fsys)
	if err != nil {
		return err
	}

	// create the catalog if it does not exist
	exists, err := wsSet.Root.HasCatalog(catalogName)
	if err != nil {
		return err
	}
	if !exists {
		err := wsSet.Root.CreateCatalog(catalogName)
		if err != nil {
			return err
		}
	}

	// get the module, release, and item values (in format `module:release:item`)
	catalogRefSplit := strings.Split(catalogRefStr, ":")
	if len(catalogRefSplit) != 3 {
		return fmt.Errorf("invalid catalog reference %q", catalogRefStr)
	}
	moduleName := catalogRefSplit[0]
	releaseName := catalogRefSplit[1]
	itemName := catalogRefSplit[2]

	ref := wfapi.CatalogRef{
		ModuleName:  wfapi.ModuleName(moduleName),
		ReleaseName: wfapi.ReleaseName(releaseName),
		ItemName:    wfapi.ItemLabel(itemName),
	}

	cat, err := wsSet.Root.OpenCatalog(&catalogName)
	if err != nil {
		return fmt.Errorf("failed to open catalog %q: %s", catalogName, err)
	}

	switch packType {
	case "tar":
		// perform rio scan to determine the ware id of the provided item
		scanWareId, err := scanWareId(wfapi.Packtype(packType), wfapi.WarehouseAddr(url))
		if err != nil {
			return fmt.Errorf("scanning %q failed: %s", url, err)
		}

		err = cat.AddItem(ref, scanWareId, c.Bool("force"))
		if err != nil {
			return fmt.Errorf("failed to add item to catalog: %s", err)
		}
		err = cat.AddByWareMirror(ref, scanWareId, wfapi.WarehouseAddr(url))
		if err != nil {
			return fmt.Errorf("failed to add mirror: %s", err)
		}
	case "git":
		if c.Args().Len() != 4 {
			return fmt.Errorf("no git reference provided")
		}
		refStr := c.Args().Get(3)

		// open the remote and list all references
		remote := git.NewRemote(memory.NewStorage(), &config.RemoteConfig{
			Name: "origin",
			URLs: []string{url},
		})
		refs, err := remote.List(&git.ListOptions{})
		if err != nil {
			return err
		}

		// find the requested reference by short name
		var gitRef *plumbing.Reference = nil
		for _, r := range refs {
			if r.Name().Short() == refStr {
				gitRef = r
				break
			}
		}
		if gitRef == nil {
			// no matching reference found
			return fmt.Errorf("git reference %q not found in repository %q", refStr, url)
		}

		// found a matching ref, add it
		wareId := wfapi.WareID{
			Packtype: "git",
			Hash:     gitRef.Hash().String(),
		}
		err = cat.AddItem(ref, wareId, c.Bool("force"))
		if err != nil {
			return fmt.Errorf("failed to add item to catalog: %s", err)
		}
		err = cat.AddByModuleMirror(ref, wfapi.Packtype(packType), wfapi.WarehouseAddr(url))
		if err != nil {
			return fmt.Errorf("failed to add mirror: %s", err)
		}

	default:
		return fmt.Errorf("unsupported packtype: %q", packType)
	}

	if c.Bool("verbose") {
		fmt.Fprintf(c.App.Writer, "added item to catalog %q\n", wsSet.Root.CatalogPath(&catalogName))

	}

	return nil
}

func cmdCatalogLs(c *cli.Context) error {
	fsys := os.DirFS("/")

	wsSet, err := openWorkspaceSet(fsys)
	if err != nil {
		return err
	}

	// get the list of catalogs in this workspace
	catalogs, err := wsSet.Root.ListCatalogs()
	if err != nil {
		return fmt.Errorf("failed to list catalogs: %s", err)
	}

	// print the list
	for _, catalog := range catalogs {
		if catalog != nil {
			fmt.Fprintf(c.App.Writer, "%s\n", *catalog)
		}
	}
	return nil
}

func gatherCatalogRefs(plot wfapi.Plot) []wfapi.CatalogRef {
	refs := []wfapi.CatalogRef{}

	// gather this plot's inputs
	for _, input := range plot.Inputs.Values {
		if input.Basis().CatalogRef != nil {
			refs = append(refs, *input.Basis().CatalogRef)
		}
	}

	// gather subplot inputs
	for _, step := range plot.Steps.Values {
		if step.Plot != nil {
			// recursively gather the refs from subplot(s)
			newRefs := gatherCatalogRefs(*step.Plot)

			// deduplicate
			unique := true
			for _, newRef := range newRefs {
				for _, existingRef := range refs {
					if newRef == existingRef {
						unique = false
						break
					}
				}
				if unique {
					refs = append(refs, newRef)
				}
			}
		}
	}

	return refs
}

func cmdCatalogBundle(c *cli.Context) error {
	fsys := os.DirFS("/")

	wsSet, err := openWorkspaceSet(fsys)
	if err != nil {
		return err
	}

	pwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get pwd: %s", err)
	}
	pwd = pwd[1:] // Drop leading slash, for use with fs package.

	plot, err := dab.PlotFromFile(fsys, filepath.Join(pwd, dab.MagicFilename_Plot))
	if err != nil {
		return err
	}

	refs := gatherCatalogRefs(plot)

	catalogPath := filepath.Join(pwd, ".warpforge", "catalog")
	// create a catalog if it does not exist
	if _, err = fs.Stat(fsys, catalogPath); os.IsNotExist(err) {
		err = os.MkdirAll("/"+catalogPath, 0755)
		if err != nil {
			return fmt.Errorf("failed to create catalog directory: %s", err)
		}

		// we need to reopen the workspace set after creating the directory
		wsSet, err = openWorkspaceSet(fsys)
		if err != nil {
			return err
		}
	}

	for _, ref := range refs {
		wareId, wareAddr, err := wsSet.Root.GetCatalogWare(ref)
		if err != nil {
			return err
		}

		if wareId == nil {
			return fmt.Errorf("could not find catalog entry for %s:%s:%s",
				ref.ModuleName, ref.ReleaseName, ref.ItemName)
		}

		fmt.Fprintf(c.App.Writer, "bundled \"%s:%s:%s\"\n", ref.ModuleName, ref.ReleaseName, ref.ItemName)
		cat, err := wsSet.Stack[0].OpenCatalog(nil)
		if err != nil {
			return fmt.Errorf("failed to open catalog: %s", err)
		}
		cat.AddItem(ref, *wareId, c.Bool("force"))
		if wareAddr != nil {
			cat.AddByWareMirror(ref, *wareId, *wareAddr)
		}
	}

	return nil
}

func installDefaultRemoteCatalog(c *cli.Context, path string) error {
	// install our default remote catalog as "default-remote" by cloning from git
	// this will noop if the catalog already exists

	defaultCatalogPath := filepath.Join(path, "mincatalog")
	if _, err := os.Stat(defaultCatalogPath); !os.IsNotExist(err) {
		// a dir exists for this catalog, do nothing
		return nil
	}

	if !c.Bool("quiet") {
		fmt.Fprintf(c.App.Writer, "installing default catalog to %s...", defaultCatalogPath)
	}
	_, err := git.PlainClone(defaultCatalogPath, false, &git.CloneOptions{
		URL: defaultCatalogUrl,
	})

	if !c.Bool("quiet") {
		fmt.Fprintf(c.App.Writer, " done.\n")
	}

	if err != nil {
		return err
	}

	return nil
}

func cmdCatalogUpdate(c *cli.Context) error {
	fsys := os.DirFS("/")

	wss, err := openWorkspaceSet(fsys)
	if err != nil {
		return fmt.Errorf("failed to open workspace set: %s", err)
	}

	// get the catalog path for the root workspace
	catalogPath := filepath.Join("/", wss.Root.CatalogBasePath())
	// create the path if it does not exist
	if _, err := os.Stat(catalogPath); os.IsNotExist(err) {
		err = os.MkdirAll(catalogPath, 0755)
		if err != nil {
			return fmt.Errorf("failed to create catalog path: %s", err)
		}
	}

	err = installDefaultRemoteCatalog(c, catalogPath)
	if err != nil {
		return fmt.Errorf("failed to install default catalog: %s", err)
	}

	catalogs, err := os.ReadDir(catalogPath)
	if err != nil {
		return fmt.Errorf("failed to list catalog path: %s", err)
	}

	for _, cat := range catalogs {
		if !cat.IsDir() {
			// ignore non-directory items
			continue
		}

		path := filepath.Join(catalogPath, cat.Name())

		r, err := git.PlainOpen(path)
		if err == git.ErrRepositoryNotExists {
			if !c.Bool("quiet") {
				fmt.Fprintf(c.App.Writer, "%s: local catalog\n", cat.Name())
			}
			continue
		} else if err != nil {
			return fmt.Errorf("failed to open git repo: %s", err)
		}

		wt, err := r.Worktree()
		if err != nil {
			return fmt.Errorf("failed to open git worktree: %s", err)
		}

		err = wt.Pull(&git.PullOptions{})
		if err == git.NoErrAlreadyUpToDate {
			if !c.Bool("quiet") {
				fmt.Fprintf(c.App.Writer, "%s: already up to date\n", cat.Name())
			}
		} else if err != nil {
			return fmt.Errorf("failed to pull from git: %s", err)
		} else {
			if !c.Bool("quiet") {
				fmt.Fprintf(c.App.Writer, "%s: updated\n", cat.Name())
			}
		}
	}

	return nil
}

func cmdCatalogRelease(c *cli.Context) error {
	logger := logging.NewLogger(c.App.Writer, c.App.ErrWriter, c.Bool("json"), c.Bool("quiet"), c.Bool("verbose"))
	ctx := logger.WithContext(c.Context)

	traceProvider, err := configTracer(c.String("trace"))
	if err != nil {
		return fmt.Errorf("could not initialize tracing: %w", err)
	}
	defer traceShutdown(c.Context, traceProvider)
	tr := otel.Tracer(TRACER_NAME)
	ctx, span := tr.Start(ctx, c.Command.FullName())
	defer span.End()

	if c.Args().Len() != 1 {
		return fmt.Errorf("invalid input. usage: warpforge catalog release [release name]")
	}
	catalogName := c.String("name")

	fsys := os.DirFS("/")

	// open the workspace set
	wsSet, err := openWorkspaceSet(fsys)
	if err != nil {
		return err
	}

	// create the catalog if it does not exist
	exists, err := wsSet.Root.HasCatalog(catalogName)
	if err != nil {
		return err
	}
	if !exists {
		err := wsSet.Root.CreateCatalog(catalogName)
		if err != nil {
			return err
		}
	}

	// get the module, release, and item values (in format `module:release:item`)
	module, err := dab.ModuleFromFile(fsys, "module.wf")
	if err != nil {
		return err
	}

	releaseName := c.Args().Get(0)

	fmt.Printf("building replay for module = %q, release = %q, executing plot...\n", module.Name, releaseName)
	plot, err := dab.PlotFromFile(fsys, dab.MagicFilename_Plot)
	if err != nil {
		return err
	}

	config := wfapi.PlotExecConfig{
		Recursive: false,
	}
	results, err := plotexec.Exec(ctx, wsSet, wfapi.PlotCapsule{Plot: &plot}, config)
	if err != nil {
		return err
	}

	cat, err := wsSet.Root.OpenCatalog(&catalogName)
	if err != nil {
		return err
	}

	ref := wfapi.CatalogRef{
		ModuleName:  module.Name,
		ReleaseName: wfapi.ReleaseName(releaseName),
		ItemName:    wfapi.ItemLabel(""), // replay is not item specific
	}

	for itemName, wareId := range results.Values {
		ref := wfapi.CatalogRef{
			ModuleName:  module.Name,
			ReleaseName: wfapi.ReleaseName(releaseName),
			ItemName:    wfapi.ItemLabel(itemName),
		}

		fmt.Println(ref.String(), "->", wareId)
		err := cat.AddItem(ref, wareId, c.Bool("force"))
		if err != nil {
			return err
		}
	}

	err = cat.AddReplay(ref, plot, c.Bool("force"))
	if err != nil {
		return err
	}

	return nil
}

func cmdIngestGitTags(c *cli.Context) error {
	if c.Args().Len() != 3 {
		return fmt.Errorf("invalid input. usage: warpforge catalog ingest-git-repo [module name] [url] [item name]")
	}
	moduleName := c.Args().Get(0)
	url := c.Args().Get(1)
	itemName := c.Args().Get(2)

	fsys := os.DirFS("/")

	// open the remote and list all references
	remote := git.NewRemote(memory.NewStorage(), &config.RemoteConfig{
		Name: "origin",
		URLs: []string{url},
	})
	refs, err := remote.List(&git.ListOptions{})
	if err != nil {
		return err
	}

	// open the workspace set and catalog
	catalogName := c.String("name")
	wsSet, err := openWorkspaceSet(fsys)
	if err != nil {
		return err
	}
	cat, err := wsSet.Root.OpenCatalog(&catalogName)
	if err != nil {
		return fmt.Errorf("failed to open catalog %q: %s", catalogName, err)
	}

	for _, ref := range refs {
		var err wfapi.Error
		if ref.Name().IsTag() {
			catalogRef := wfapi.CatalogRef{
				ModuleName:  wfapi.ModuleName(moduleName),
				ReleaseName: wfapi.ReleaseName(ref.Name().Short()),
				ItemName:    wfapi.ItemLabel(itemName),
			}
			wareId := wfapi.WareID{
				Packtype: "git",
				Hash:     ref.Hash().String(),
			}
			err = cat.AddItem(catalogRef, wareId, c.Bool("force"))
			if err != nil && err.(*wfapi.ErrorVal).Code() == "warpforge-error-catalog-item-already-exists" {
				fmt.Printf("catalog already has item %s:%s:%s\n", catalogRef.ModuleName,
					catalogRef.ReleaseName, catalogRef.ItemName)
				continue
			} else if err != nil {
				return fmt.Errorf("failed to add item to catalog: %s", err)
			}
			err = cat.AddByModuleMirror(catalogRef, wfapi.Packtype("git"), wfapi.WarehouseAddr(url))
			if err != nil {
				return fmt.Errorf("failed to add mirror: %s", err)
			}
			fmt.Printf("adding item %s:%s:%s \t-> %s\n", catalogRef.ModuleName,
				catalogRef.ReleaseName, catalogRef.ItemName, wareId)

		}
	}

	return nil
}
