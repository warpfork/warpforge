package workspace

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/serum-errors/go-serum"

	"github.com/warptools/warpforge/pkg/dab"
	"github.com/warptools/warpforge/wfapi"
)

const (
	magicWorkspaceDirname = dab.MagicFilename_Workspace
	magicHomeWorkspaceDirname = dab.MagicFilename_HomeWorkspace
)

var homedir string

func init() {
	var err error
	// Assign homedir.
	//  Somewhat complicated by the fact we non-rooted paths internally for consistency
	//   (which is in turn driven largely by stdlib's `testing/testfs` not supporting them).
	homedir, err = os.UserHomeDir()
	homedir = filepath.Clean(homedir)
	if err != nil {
		serr := wfapi.ErrorSearchingFilesystem("homedir", err).(serum.ErrorInterface)
		wfapi.TerminalError(serr, 9)
	}
	if homedir == "" {
		homedir = "home" // dummy, just to avoid the irritant of empty strings.
	}
	if homedir[0] == '/' { // de-rootify this, for ease of comparison with other derootified paths.
		homedir = homedir[1:]
	}
}

// FindWorkspace looks for a workspace on the filesystem and returns the first one found,
// searching directories upward.
//
// It searches from `join(basisPath,searchPath)` up to `basisPath`
// (in other words, it won't search above basisPath).
// Invoking it with an empty string for `basisPath` and cwd for `searchPath` is typical.
//
// If no workspace is found, it will return nil for both the workspace pointer and error value.
// If errors are returned, they're due to filesystem IO.
// FindWorkspace will ignore your home workspace and carry on searching upwards.
//
// An fsys handle is required, but is typically `os.DirFS("/")` outside of tests.
//
// Errors:
//
//    - warpforge-error-searching-filesystem -- when an unexpected error occurs traversing the search path
func FindWorkspace(fsys fs.FS, basisPath, searchPath string) (ws *Workspace, remainingSearchPath string, err error) {
	// Our search loops over searchPath, popping a path segment off at the end of every round.
	//  Keep the given searchPath in hand; we might need it for an error report.
	searchAt := searchPath
	for {
		// Assume the search path exists and is a dir (we'll get a reasonable error anyway if it's not);
		//  join that path with our search target and try to open it.
		_, err := statDir(fsys, filepath.Join(basisPath, searchAt, magicWorkspaceDirname))
		if err == nil {
			ws := openWorkspace(fsys, filepath.Join(basisPath, searchAt))
			return ws, filepath.Dir(searchAt), nil
		}
		if errors.Is(err, fs.ErrNotExist) { // no such thing?  oh well.  pop a segment and keep looking.
			searchAt = filepath.Dir(searchAt)
			// If popping a searchAt segment got us down to nothing,
			//  and we didn't find anything here either,
			//   that's it: return NotFound.
			if searchAt == "/" || searchAt == "." {
				return nil, "", nil
			}
			// ... otherwise: continue, with popped searchAt.
			continue
		}
		// You're still here?  That means there's an error, but of some unpleasant kind.
		//  Whatever this error is, our search has blind spots: error out.
		return nil, searchAt, wfapi.ErrorSearchingFilesystem("workspace", err)
	}
}

// statDir is fs.Stat but returns fs.ErrNotExist if the path is not a dir
func statDir(fsys fs.FS, path string) (fs.FileInfo, error) {
	fi, err := fs.Stat(fsys, path)
	if err != nil {
		return fi, err
	}
	if !fi.IsDir() {
		return fi, fs.ErrNotExist
	}
	return fi, err
}

// FindWorkspaceStack works similarly to FindWorkspace, but finds all workspaces, not just the nearest one.
// The first element of the returned slice is the nearest workspace; subsequent elements are its parents, then grandparents, etc.
// The last element of the returned slice is the root workspace.
// If no root workspace is found then the last element will be the home workspace (or at the most extreme: where the home workspace *should be*).
//
// It searches from `join(basisPath,searchPath)` up to `basisPath`
// (in other words, it won't search above basisPath).
// Invoking it with an empty string for `basisPath` and cwd for `searchPath` is typical.
//
// An fsys handle is required, but is typically `os.DirFS("/")` outside of tests.
//
// Errors:
//
//    - warpforge-error-searching-filesystem -- when an unexpected error occurs traversing the search path
func FindWorkspaceStack(fsys fs.FS, basisPath, searchPath string) (wss WorkspaceSet, err error) {
	// Repeatedly apply FindWorkspace and stack stuff up.
	for {
		var ws *Workspace
		ws, searchPath, err = FindWorkspace(fsys, basisPath, searchPath)
		if err != nil {
			return
		}
		if ws == nil {
			break
		}
		wss = append(wss, ws)
		if ws.IsRootWorkspace() {
			break
		}
	}
	// If no root workspace was found, include the home workspace at the end of the stack.
	if len(wss) == 0 || !wss[len(wss)-1].IsRootWorkspace() {
		wss = append(wss, openHomeWorkspace(fsys))
	}
	return wss, nil
}

// FindRootWorkspace calls FindWorkspaceStack and returns the root workspace.
//
// A root workspace is marked by containing a file named "root"
//
// If no root filesystems are marked, this will default to the last item in the
// stack, which is the home workspace.
//
// An fsys handle is required, but is typically `os.DirFS("/")` outside of tests.
//
// Errors:
//
//    - warpforge-error-searching-filesystem -- when an error occurs while searching for the workspace
func FindRootWorkspace(fsys fs.FS, basisPath string, searchPath string) (*Workspace, error) {
	stack, err := FindWorkspaceStack(fsys, basisPath, searchPath)
	if err != nil {
		return nil, err
	}

	for _, ws := range stack {
		if ws.IsRootWorkspace() {
			// this is our root workspace so we're done
			return ws, nil
		}
	}
	panic("FindWorkspaceStack must return a root workspace.")
}

// checkIsRootWorkspace returns true if the workspace contains the magic "root" file.
func checkIsRootWorkspace(fsys fs.FS, rootPath string) bool {
	// check if the root marker file exists
	_, err := fs.Stat(fsys, filepath.Join(rootPath, magicWorkspaceDirname, "root"))
	return err == nil
}

type PlaceWorkspaceOpt func(rootPath string) error

func SetRootWorkspaceOpt() PlaceWorkspaceOpt {
	return func(rootPath string) error {
		rootMagicFile := filepath.Join(rootPath, magicWorkspaceDirname, "root")
		f, err := os.Create(rootMagicFile)
		f.Close()
		if err != nil {
			return wfapi.ErrorIo("cannot make workspace root indicator", rootMagicFile, err)
		}
		return nil
	}
}

// PlaceWorkspace places the directory structure down on the filesystem for a workspace to be detected.
//
// Errors:
//
//    - warpforge-error-io -- when creating workspace fails
func PlaceWorkspace(rootPath string, opts ...PlaceWorkspaceOpt) error {
	workspaceDirname := magicWorkspaceDirname

	fi, err := os.Stat(rootPath)
	if err != nil {
		return wfapi.ErrorIo("invalid rootpath for workspace", rootPath, err)
	}
	if !fi.IsDir() {
		return wfapi.ErrorIo("workspace rootpath is not a directory", rootPath, err)
	}

	workspaceDirname = filepath.Join(rootPath, workspaceDirname)
	if err := os.MkdirAll(workspaceDirname, 0755|os.ModeDir); err != nil {
		return wfapi.ErrorIo("could not create workspace internals directory", workspaceDirname, err)
	}

	for _, o := range opts {
		if err := o(rootPath); err != nil {
			return err
		}
	}
	return nil
}
