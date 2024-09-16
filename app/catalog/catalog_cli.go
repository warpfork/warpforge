package catalogcli

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/serum-errors/go-serum"
	"github.com/urfave/cli/v2"
	"go.opentelemetry.io/otel/trace"

	appbase "github.com/warptools/warpforge/app/base"
	"github.com/warptools/warpforge/app/base/util"
	"github.com/warptools/warpforge/pkg/cataloghtml"
	"github.com/warptools/warpforge/pkg/config"
	"github.com/warptools/warpforge/pkg/dab"
	"github.com/warptools/warpforge/pkg/logging"
	"github.com/warptools/warpforge/pkg/mirroring"
	"github.com/warptools/warpforge/pkg/plotexec"
	"github.com/warptools/warpforge/pkg/tracing"
	"github.com/warptools/warpforge/wfapi"
)

func init() {
	appbase.App.Commands = append(appbase.App.Commands, catalogCmdDef)
}

var catalogCmdDef = &cli.Command{
	Name:  "catalog",
	Usage: "Subcommands that operate on catalogs",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:    "name",
			Aliases: []string{"n"},
			Usage:   "Name of the catalog to operate on",
			Value:   "default",
		},
		&cli.BoolFlag{
			Name:    "force",
			Aliases: []string{"f"},
			Usage:   "Force operation, even if it causes data to be overwritten.",
		},
	},
	Subcommands: []*cli.Command{
		{
			Name:  "init",
			Usage: "Creates a named catalog in the root workspace",
			Action: util.ChainCmdMiddleware(cmdCatalogInit,
				util.CmdMiddlewareLogging,
				util.CmdMiddlewareTracingConfig,
				util.CmdMiddlewareTracingSpan,
			),
		},
		{
			Name:  "add",
			Usage: "Add an item to the given catalog in the root workspace. Will create a catalog if required.",
			Action: util.ChainCmdMiddleware(cmdCatalogAdd,
				util.CmdMiddlewareLogging,
				util.CmdMiddlewareTracingConfig,
				util.CmdMiddlewareTracingSpan,
			),
		},
		{
			Name:  "release",
			Usage: "Add a module to the root workspace catalog as a new release",
			Action: util.ChainCmdMiddleware(cmdCatalogRelease,
				util.CmdMiddlewareLogging,
				util.CmdMiddlewareTracingConfig,
				util.CmdMiddlewareTracingSpan,
			),
		},
		{
			Name:      "ls",
			Usage:     "List available catalogs in the root workspace and their contents",
			ArgsUsage: "[catalog] [module] [release] [item]",
			Action: util.ChainCmdMiddleware(cmdCatalogLs,
				util.CmdMiddlewareLogging,
				util.CmdMiddlewareTracingConfig,
				util.CmdMiddlewareTracingSpan,
			),
		},
		{
			Name:  "show",
			Usage: "Show the contents of a module in the root workspace catalog",
			Action: util.ChainCmdMiddleware(cmdCatalogShow,
				util.CmdMiddlewareLogging,
				util.CmdMiddlewareTracingConfig,
				util.CmdMiddlewareTracingSpan,
			),
		},
		{
			Name:  "bundle",
			Usage: "Bundle required catalog items into the local workspace.",
			Action: util.ChainCmdMiddleware(cmdCatalogBundle,
				util.CmdMiddlewareLogging,
				util.CmdMiddlewareTracingConfig,
				util.CmdMiddlewareTracingSpan,
			),
		},
		{
			Name:  "update",
			Usage: "Update remote catalogs in the root workspace. Will install the default warpsys catalog.",
			Action: util.ChainCmdMiddleware(cmdCatalogUpdate,
				util.CmdMiddlewareLogging,
				util.CmdMiddlewareTracingConfig,
				util.CmdMiddlewareTracingSpan,
			),
		},
		{
			Name:  "ingest-git-tags",
			Usage: "Ingest all tags from a git repository into a root workspace catalog entry",
			Action: util.ChainCmdMiddleware(cmdIngestGitTags,
				util.CmdMiddlewareLogging,
				util.CmdMiddlewareTracingConfig,
				util.CmdMiddlewareTracingSpan,
			),
		},
		{
			Name:  "generate-html",
			Usage: "Generates HTML output for the root workspace catalog containing information on modules",
			Action: util.ChainCmdMiddleware(cmdGenerateHtml,
				util.CmdMiddlewareLogging,
				util.CmdMiddlewareTracingConfig,
				util.CmdMiddlewareTracingSpan,
			),
			Flags: []cli.Flag{
				&cli.StringFlag{
					Name:    "output",
					Aliases: []string{"o"},
					Usage:   "Output path for HTML generation",
				},
				&cli.StringFlag{
					Name:  "url-prefix",
					Usage: "URL prefix for links within generated HTML",
				},
				&cli.StringFlag{
					Name:  "download-url",
					Usage: "URL for warehouse to use for download links",
				},
			},
		},
		{
			Name:  "mirror",
			Usage: "Mirror the contents of a catalog to remote warehouses",
			Action: util.ChainCmdMiddleware(cmdMirror,
				util.CmdMiddlewareLogging,
				util.CmdMiddlewareTracingConfig,
				util.CmdMiddlewareTracingSpan,
			),
		},
		{
			Name:  "serve",
			Usage: "Generates html and serves",
			Action: util.ChainCmdMiddleware(cmdServe,
				util.CmdMiddlewareLogging,
				util.CmdMiddlewareTracingConfig,
				util.CmdMiddlewareTracingSpan,
			),
			Flags: []cli.Flag{
				&cli.StringFlag{
					Name:    "output",
					Aliases: []string{"o"},
					Usage:   "Output path for HTML generation",
				},
				&cli.StringFlag{
					Name:  "url-prefix",
					Usage: "URL prefix for links within generated HTML",
					Value: "/",
				},
				&cli.StringFlag{
					Name:  "download-url",
					Usage: "URL for warehouse to use for download links",
				},
				&cli.UintFlag{
					Name:    "port",
					Aliases: []string{"p"},
					Usage:   "Port number for the server address",
					Value:   8080,
				},
			},
		},
	},
}

func scanWareId(ctx context.Context, packType wfapi.Packtype, addr wfapi.WarehouseAddr) (wfapi.WareID, error) {
	result := wfapi.WareID{}
	binPath, err := config.BinPath()
	if err != nil {
		return result, fmt.Errorf("failed to get path to rio")
	}
	rioPath := filepath.Join(binPath, "rio")
	cmdCtx, cmdSpan := tracing.Start(ctx, "rio scan", trace.WithAttributes(tracing.AttrFullExecNameRio))
	defer cmdSpan.End()
	rioScan := exec.CommandContext(
		cmdCtx, rioPath, "scan", "--source="+string(addr), string(packType),
	)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	rioScan.Stdout = &stdout
	rioScan.Stderr = &stderr
	cmdErr := rioScan.Run()
	tracing.EndWithStatus(cmdSpan, cmdErr)
	if cmdErr != nil {
		return result, fmt.Errorf("failed to run rio scan command: %s\n%s", cmdErr, stderr.String())
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
	var err error
	wss, err := util.OpenWorkspaceSet()
	if err != nil {
		return err
	}
	catalogPath, err := wss.Root().CatalogPath(catalogName)
	if err != nil {
		return err
	}
	catalogPath = filepath.Join("/", catalogPath)

	// check if the catalog directory exists
	_, err = os.Stat(catalogPath)
	if !os.IsNotExist(err) {
		if err == nil {
			// catalog already exists
			return fmt.Errorf("catalog %q already exists (path: %q)", catalogName, catalogPath)
		}
		return fmt.Errorf("catalog %q already exists (path: %q): %w", catalogName, catalogPath, err)
	}
	// catalog does not exist, create the dir
	if err := os.MkdirAll(catalogPath, 0755); err != nil {
		// stat or mkdir failed
		return fmt.Errorf("failed to create catalog: %s", err)
	}

	return nil
}

func cmdCatalogAdd(c *cli.Context) error {
	if c.Args().Len() < 3 {
		return fmt.Errorf("invalid input. usage: warpforge catalog add [pack type] [catalog ref] [url] [ref]")
	}
	ctx := c.Context
	catalogName := c.String("name")

	packType := c.Args().Get(0)
	catalogRefStr := c.Args().Get(1)
	url := c.Args().Get(2)

	// open the workspace set
	wsSet, err := util.OpenWorkspaceSet()
	if err != nil {
		return err
	}

	// create the catalog if it does not exist
	exists, err := wsSet.Root().HasCatalog(catalogName)
	if err != nil {
		return err
	}
	if !exists {
		err := wsSet.Root().CreateCatalog(catalogName)
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

	root := wsSet.Root()
	cat, err := root.OpenCatalog(catalogName)
	if err != nil {
		return fmt.Errorf("failed to open catalog %q: %s", catalogName, err)
	}

	switch packType {
	case "tar":
		// perform rio scan to determine the ware id of the provided item
		scanWareId, err := scanWareId(ctx, wfapi.Packtype(packType), wfapi.WarehouseAddr(url))
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
		remote := git.NewRemote(memory.NewStorage(), &gitconfig.RemoteConfig{
			Name: "origin",
			URLs: []string{url},
		})
		listCtx, listSpan := tracing.Start(ctx, "git ls-remote", trace.WithAttributes(tracing.AttrFullExecNameGit, tracing.AttrFullExecOperationGitLs))
		defer listSpan.End()
		refs, err := remote.ListContext(listCtx, &git.ListOptions{})
		tracing.EndWithStatus(listSpan, err)
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
		catalogPath, _ := root.CatalogPath(catalogName) // assume an error would be handled earlier
		fmt.Fprintf(c.App.Writer, "added item to catalog %q\n", catalogPath)
	}

	return nil
}

func cmdCatalogLs(c *cli.Context) error {
	wsSet, err := util.OpenWorkspaceSet()
	if err != nil {
		return err
	}

	if c.NArg() == 0 {
		// get the list of catalogs in this workspace
		catalogs, err := wsSet.Root().ListCatalogs()
		if err != nil {
			return fmt.Errorf("failed to list catalogs: %s", err)
		}

		// print the list
		for _, catalog := range catalogs {
			if catalog != "" {
				fmt.Fprintf(c.App.Writer, "%s\n", catalog)
			}
		}
		return nil
	}

	currentArg := 0
	nextPosArg := func() (string, bool) {
		if currentArg > c.NArg() {
			return "", false
		}
		arg := c.Args().Get(currentArg)
		currentArg++
		return arg, currentArg < c.NArg()
	}

	catalogName, moreArgs := nextPosArg()
	catalog, err := wsSet.Root().OpenCatalog(catalogName)
	if err != nil {
		return fmt.Errorf("failed to find catalog: %s", err)
	}
	if !moreArgs {
		// get list of modules in this catalog
		for _, module := range catalog.Modules() {
			fmt.Fprintf(c.App.Writer, "%s\n", module)
		}
		return nil
	}

	moduleName, moreArgs := nextPosArg()
	ref := wfapi.CatalogRef{
		ModuleName: wfapi.ModuleName(moduleName),
	}
	module, err := catalog.GetModule(ref)
	if err != nil {
		return fmt.Errorf("failed to find module: %s", err)
	}
	if !moreArgs {
		// get list of releases in a module
		for _, release := range module.Releases.Keys {
			fmt.Fprintf(c.App.Writer, "%s\n", release)
		}
		return nil
	}

	releaseName, moreArgs := nextPosArg()
	ref.ReleaseName = wfapi.ReleaseName(releaseName)
	release, err := catalog.GetRelease(ref)
	if err != nil {
		return fmt.Errorf("failed to find release: %s", err)
	}
	if !moreArgs {
		// list ware IDs for items
		for _, label := range release.Items.Keys {
			item := release.Items.Values[label]
			fmt.Fprintf(c.App.Writer, "%s\t%s\n", item, label)
		}
		return nil
	}
	// after this idk, we could list mirrors or replays or... nothing.
	// this seems like a good ending point due to ambiguity of meaning
	return fmt.Errorf("too many args")
}

func cmdCatalogBundle(c *cli.Context) error {
	var err error
	wsSet, err := util.OpenWorkspaceSet()
	if err != nil {
		return err
	}
	pwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get pwd: %s", err)
	}
	plot, err := util.PlotFromFile(filepath.Join(pwd, dab.MagicFilename_Plot))
	if err != nil {
		return err
	}

	if err := wsSet.Tidy(c.Context, plot, c.Bool("force")); err != nil {
		return err
	}

	return nil
}

func cmdCatalogUpdate(c *cli.Context) error {
	var err error
	wss, err := util.OpenWorkspaceSet()
	if err != nil {
		return fmt.Errorf("failed to open workspace set: %s", err)
	}

	// get the catalog path for the root workspace
	catalogPath := filepath.Join("/", wss.Root().CatalogBasePath())
	// create the path if it does not exist
	if _, err := os.Stat(catalogPath); os.IsNotExist(err) {
		err = os.MkdirAll(catalogPath, 0755)
		if err != nil {
			return fmt.Errorf("failed to create catalog path: %s", err)
		}
	}

	if err = InstallDefaultRemoteCatalog(c.Context, catalogPath); err != nil {
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

const defaultCatalogUrl = "https://github.com/warptools/warpsys-catalog.git"

// InstallDefaultRemoteCatalog creates the default catalog by cloning a remote catalog over network.
// This function will do nothing if the default catalog already exists.
//
// Errors:
//
//    - warpforge-error-git -- Cloning catalog fails
//    - warpforge-error-io -- catalog path exists but is in a strange state
func InstallDefaultRemoteCatalog(ctx context.Context, path string) error {
	log := logging.Ctx(ctx)
	// install our default remote catalog as "default-remote" by cloning from git
	// this will noop if the catalog already exists
	defaultCatalogPath := filepath.Join(path, "warpsys")
	_, err := os.Stat(defaultCatalogPath)
	if !os.IsNotExist(err) {
		if err == nil {
			// a dir exists for this catalog, do nothing
			return nil
		}
		return wfapi.ErrorIo("unknown error with catalog path", defaultCatalogPath, err)
	}

	log.Info("", "installing default catalog to %s...", defaultCatalogPath)

	gitCtx, gitSpan := tracing.Start(ctx, "clone catalog", trace.WithAttributes(tracing.AttrFullExecNameGit, tracing.AttrFullExecOperationGitClone))
	defer gitSpan.End()
	_, err = git.PlainCloneContext(gitCtx, defaultCatalogPath, false, &git.CloneOptions{
		URL: defaultCatalogUrl,
	})
	tracing.EndWithStatus(gitSpan, err)

	log.Info("", "installing default catalog complete")

	if err != nil {
		return wfapi.ErrorGit("Unable to git clone catalog", err)
	}
	return nil
}

func cmdCatalogRelease(c *cli.Context) error {
	ctx := c.Context
	var err error

	if c.Args().Len() != 1 {
		return fmt.Errorf("invalid input. usage: warpforge catalog release [release name]")
	}
	catalogName := c.String("name")
	wss, err := util.OpenWorkspaceSet()
	if err != nil {
		return err
	}
	rootWs := wss.Root()
	// create the catalog if it does not exist
	exists, err := rootWs.HasCatalog(catalogName)
	if err != nil {
		return err
	}
	if !exists {
		err := rootWs.CreateCatalog(catalogName)
		if err != nil {
			return err
		}
	}

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	fsys := os.DirFS(cwd)
	// get the module, release, and item values (in format `module:release:item`)
	module, err := dab.ModuleFromFile(fsys, dab.MagicFilename_Module)
	if err != nil {
		return err
	}

	releaseName := c.Args().Get(0)

	fmt.Printf("building replay for module = %q, release = %q, executing plot...\n", module.Name, releaseName)
	plot, err := util.PlotFromFile(dab.MagicFilename_Plot)
	if err != nil {
		return err
	}

	execCfg, err := config.PlotExecConfig(nil)
	if err != nil {
		return err
	}
	results, err := plotexec.Exec(ctx, execCfg, wss, wfapi.PlotCapsule{Plot: &plot}, wfapi.PlotExecConfig{Recursive: false})
	if err != nil {
		return err
	}

	cat, err := rootWs.OpenCatalog(catalogName)
	if err != nil {
		return err
	}

	parent := wfapi.CatalogRef{
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

	err = cat.AddReplay(parent, plot, c.Bool("force"))
	if err != nil {
		return err
	}

	return nil
}

func cmdIngestGitTags(c *cli.Context) error {
	if c.Args().Len() != 3 {
		return fmt.Errorf("invalid input. usage: warpforge catalog ingest-git-repo [module name] [url] [item name]")
	}
	ctx := c.Context

	moduleName := c.Args().Get(0)
	url := c.Args().Get(1)
	itemName := c.Args().Get(2)

	// open the remote and list all references
	remote := git.NewRemote(memory.NewStorage(), &gitconfig.RemoteConfig{
		Name: "origin",
		URLs: []string{url},
	})

	listCtx, listSpan := tracing.Start(ctx, "git ls-remote", trace.WithAttributes(tracing.AttrFullExecNameGit, tracing.AttrFullExecOperationGitLs))
	defer listSpan.End()
	refs, err := remote.ListContext(listCtx, &git.ListOptions{})
	tracing.EndWithStatus(listSpan, err)
	if err != nil {
		return err
	}

	// open the workspace set and catalog
	catalogName := c.String("name")
	wsSet, err := util.OpenWorkspaceSet()
	if err != nil {
		return err
	}
	cat, err := wsSet.Root().OpenCatalog(catalogName)
	if err != nil {
		return fmt.Errorf("failed to open catalog %q: %s", catalogName, err)
	}

	for _, ref := range refs {
		var err error
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
			if err != nil && serum.Code(err) == "warpforge-error-catalog-item-already-exists" {
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

func cmdCatalogShow(c *cli.Context) error {
	if c.Args().Len() != 1 {
		return fmt.Errorf("invalid input. usage: warpforge catalog show [module name]")
	}

	wsSet, err := util.OpenWorkspaceSet()
	if err != nil {
		return err
	}

	catalogName := c.String("name")
	cat, err := wsSet.Root().OpenCatalog(catalogName)
	if err != nil {
		return fmt.Errorf("failed to open catalog %q: %s", catalogName, err)
	}

	searchStr := c.Args().First()

	ref := wfapi.CatalogRef{
		ModuleName:  wfapi.ModuleName(searchStr),
		ReleaseName: wfapi.ReleaseName(""),
		ItemName:    wfapi.ItemLabel(""),
	}

	mod, err := cat.GetModule(ref)
	if err != nil {
		return fmt.Errorf("failed to get module %q: %s", ref.ModuleName, err)
	}

	if mod == nil {
		fmt.Printf("module %q not found\n", ref.ModuleName)
		return nil
	}

	fmt.Println(mod.Name)
	for nr, releaseName := range mod.Releases.Keys {
		var chr string
		if nr+1 < len(mod.Releases.Keys) {
			fmt.Printf(" ├─ %s:%s\n", mod.Name, releaseName)
			chr = "│"
		} else {
			fmt.Printf(" └─ %s:%s\n", mod.Name, releaseName)
			chr = " "
		}
		ref.ReleaseName = releaseName
		release, _ := cat.GetRelease(ref)

		for ni, itemName := range release.Items.Keys {
			if ni+1 < len(release.Items.Keys) {
				if c.Bool("verbose") {
					fmt.Printf(" %s   ├─ %s:%s:%s (%s)\n", chr, mod.Name, releaseName, itemName, release.Items.Values[itemName].String())
				} else {
					fmt.Printf(" %s   ├─ %s:%s:%s\n", chr, mod.Name, releaseName, itemName)
				}
			} else {
				if c.Bool("verbose") {
					fmt.Printf(" %s   └─ %s:%s:%s (%s)\n", chr, mod.Name, releaseName, itemName, release.Items.Values[itemName].String())
				} else {
					fmt.Printf(" %s   └─ %s:%s:%s\n", chr, mod.Name, releaseName, itemName)
				}
			}
		}
	}

	return nil
}

type protoSiteConfig struct {
	catalogName        string
	outputPathOverride string
	urlPrefix          string
	downloadUrl        string
}

func (cfg protoSiteConfig) SiteConfig(ctx context.Context) (cataloghtml.SiteConfig, error) {
	// open the workspace set
	wsSet, err := util.OpenWorkspaceSet()
	if err != nil {
		return cataloghtml.SiteConfig{}, err
	}

	exists, err := wsSet.Root().HasCatalog(cfg.catalogName)
	if err != nil {
		return cataloghtml.SiteConfig{}, err
	}
	if !exists {
		return cataloghtml.SiteConfig{}, fmt.Errorf("catalog %q not found", cfg.catalogName)
	}

	cat, err := wsSet.Root().OpenCatalog(cfg.catalogName)
	if err != nil {
		return cataloghtml.SiteConfig{}, fmt.Errorf("failed to open catalog %q: %s", cfg.catalogName, err)
	}

	// by default, output to a subdir of the catalog named `_html`
	// this can be overriden by a cli flag that provides a path
	outputPath, err := wsSet.Root().CatalogPath(cfg.catalogName)
	if err != nil {
		return cataloghtml.SiteConfig{}, err
	}
	outputPath = filepath.Join("/", outputPath, "_html")
	if cfg.outputPathOverride != "" {
		outputPath = cfg.outputPathOverride
	}

	// by default, the URL prefix is the same as the output path,
	// this works if the HTML is accessed using `file:///` URLs.
	// however, to allow for generating a hosted site, this can be
	// overridden by the CLI
	urlPrefix := outputPath
	if cfg.urlPrefix != "" {
		urlPrefix = cfg.urlPrefix
	}

	var warehouseUrl *string = nil
	if cfg.downloadUrl != "" {
		dlUrl := cfg.downloadUrl
		warehouseUrl = &dlUrl
	}

	return cataloghtml.SiteConfig{
		Ctx:         ctx,
		Cat_dab:     cat,
		OutputPath:  outputPath,
		URLPrefix:   urlPrefix,
		DownloadURL: warehouseUrl,
	}, nil
}

func cmdGenerateHtml(c *cli.Context) error {
	pcfg := protoSiteConfig{
		catalogName:        c.String("name"),
		outputPathOverride: c.String("output"),
		urlPrefix:          c.String("url-prefix"),
		downloadUrl:        c.String("download-url"),
	}
	cfg, err := pcfg.SiteConfig(c.Context)
	if err != nil {
		return err
	}

	os.RemoveAll(cfg.OutputPath)
	if err := cfg.CatalogAndChildrenToHtml(); err != nil {
		return fmt.Errorf("failed to generate html: %s", err)
	}

	fmt.Printf("published HTML for catalog %q to %s\n", pcfg.catalogName, cfg.OutputPath)

	return nil
}

func cmdServe(c *cli.Context) error {
	pcfg := protoSiteConfig{
		catalogName:        c.String("name"),
		outputPathOverride: c.String("output"),
		urlPrefix:          c.String("url-prefix"),
		downloadUrl:        c.String("download-url"),
	}
	port := c.Uint("port")
	addr := fmt.Sprintf("%s:%d", "localhost", port)

	cfg, err := pcfg.SiteConfig(c.Context)
	if err != nil {
		return err
	}
	os.RemoveAll(cfg.OutputPath)
	if err := cfg.CatalogAndChildrenToHtml(); err != nil {
		return fmt.Errorf("failed to generate html: %s", err)
	}

	fmt.Printf("published HTML for catalog %q to %s\n", pcfg.catalogName, cfg.OutputPath)

	dirHandler := http.Dir(cfg.OutputPath)
	fmt.Printf("Serving at http://%s\n", addr)
	if err := http.ListenAndServe(addr, http.FileServer(dirHandler)); err != nil {
		return err
	}
	return nil
}

func cmdMirror(c *cli.Context) error {
	ctx := c.Context
	logger := logging.Ctx(ctx)

	wsSet, err := util.OpenWorkspaceSet()
	if err != nil {
		return err
	}

	catalogName := c.String("name")
	cat, err := wsSet.Root().OpenCatalog(catalogName)
	if err != nil {
		return fmt.Errorf("failed to open catalog %q: %s", catalogName, err)
	}

	configs, err := wsSet.Root().GetMirroringConfig()
	if err != nil {
		return err
	}

	for wareAddr, cfg := range configs.Values {
		logger.Info("mirror", "mirroring to warehouse %q", wareAddr)
		err = mirroring.PushToWarehouseAddr(ctx, *wsSet.Root(), cat, wareAddr, cfg)
		if err != nil {
			return err
		}
	}

	return err
}
