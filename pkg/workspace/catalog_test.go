package workspace

import (
	"testing"
	"testing/fstest"

	qt "github.com/frankban/quicktest"

	_ "github.com/warptools/warpforge/pkg/testutil"
	"github.com/warptools/warpforge/wfapi"
)

func TestCatalogLookup(t *testing.T) {
	t.Run("catalog-lookup", func(t *testing.T) {
		moduleData := `{
	"catalogmodule.v1": {
		"name": "example.com/module",
		"metadata": {},
		"releases": {
			"v1.0": "zM5K3awreLPFS2jSHVkdWZvST3AqJqapCTpZNJbtZjjfFbTiZdFSExhjFoDrkk4bGGQY8M3"
		}
	}
}
`
		releaseData := `{
	"releaseName": "v1.0",
	"metadata": {
		"replay": "zM5K3aX2vbXSjAMaFVBAAYccoNpf3h2mQkDZLFmD7pEZdUUWtsx1qk9Dh4KoPq7zmEdR1cQ"
	},
	"items": {
		"x86_64": "tar:abcd"
	} 
}
`
		mirrorData := `{
	"catalogmirrors.v1": {
		"byWare": {
			"tar:abcd": [
				"https://example.com/module/module-v1.0-x86_64.tgz"
			]
		}
	}
}
`
		replayData := `{
	"plot.v1": {
		"inputs": {
				"rootfs": "catalog:warpsys.org/busybox:v1.35.0:amd64-static"
		},
		"steps": {
				"hello-world": {
						"protoformula": {
								"inputs": {
										"/": "pipe::rootfs"
								},
								"action": {
										"script": {
												"interpreter": "/bin/sh",
												"contents": [
														"mkdir /output",
														"echo 'hello world' | tee /output/file"
												],
												"network": false
										}
								},
								"outputs": {
										"out": {
												"from": "/output",
												"packtype": "tar"
										}
								}
						}
				}
		},
		"outputs": {
				"output": "pipe:hello-world:out"
		}
	}
}
`

		ref := wfapi.CatalogRef{
			ModuleName:  "example.com/module",
			ReleaseName: "v1.0",
			ItemName:    "x86_64",
		}

		t.Run("single-catalog-lookup", func(t *testing.T) {
			fsys := fstest.MapFS{
				"home/user/.warpforge/catalog/example.com/module/_module.json": &fstest.MapFile{
					Mode: 0644,
					Data: []byte(moduleData),
				},
				"home/user/.warpforge/catalog/example.com/module/_releases/v1.0.json": &fstest.MapFile{
					Mode: 0644,
					Data: []byte(releaseData),
				},
				"home/user/.warpforge/catalog/example.com/module/_mirrors.json": &fstest.MapFile{
					Mode: 0644,
					Data: []byte(mirrorData),
				},
			}
			var err error
			ws, _, err := FindWorkspace(fsys, "", "home/user/")
			qt.Assert(t, err, qt.IsNil)
			qt.Assert(t, ws, qt.IsNotNil)

			wareId, wareAddr, err := ws.GetCatalogWare(ref)
			qt.Assert(t, err, qt.IsNil)
			qt.Assert(t, wareId, qt.IsNotNil)
			qt.Assert(t, wareId.Hash, qt.Equals, "abcd")
			qt.Assert(t, wareId.Packtype, qt.Equals, wfapi.Packtype("tar"))
			qt.Assert(t, wareAddr, qt.Contains, wfapi.WarehouseAddr("https://example.com/module/module-v1.0-x86_64.tgz"))

		})
		t.Run("multi-catalog-lookup", func(t *testing.T) {
			fsys := fstest.MapFS{
				"home/user/.warpforge/root": &fstest.MapFile{Mode: 0644},
				"home/user/.warpforge/catalogs/test/example.com/module/_module.json": &fstest.MapFile{
					Mode: 0644,
					Data: []byte(moduleData),
				},
				"home/user/.warpforge/catalogs/test/example.com/module/_releases/v1.0.json": &fstest.MapFile{
					Mode: 0644,
					Data: []byte(releaseData),
				},
				"home/user/.warpforge/catalogs/test/example.com/module/_mirrors.json": &fstest.MapFile{
					Mode: 0644,
					Data: []byte(mirrorData),
				},
				"home/user/.warpforge/catalogs/test/example.com/module-two/_module.json": &fstest.MapFile{
					Mode: 0644,
					Data: []byte(moduleData),
				},
				"home/user/.warpforge/catalogs/test/example.com/module-two/_releases/v1.0.json": &fstest.MapFile{
					Mode: 0644,
					Data: []byte(releaseData),
				},
				"home/user/.warpforge/catalogs/test/example.com/module-two/_mirrors.json": &fstest.MapFile{
					Mode: 0644,
					Data: []byte(mirrorData),
				},
			}
			check := func(path string) func(t *testing.T) {
				return func(t *testing.T) {
					ws, _, err := FindWorkspace(fsys, "", path)
					qt.Assert(t, err, qt.IsNil)
					qt.Assert(t, ws, qt.IsNotNil)

					catName := "test"
					cat, err := ws.OpenCatalog(catName)
					qt.Assert(t, err, qt.IsNil)
					qt.Assert(t, len(cat.Modules()), qt.Equals, 2)
					qt.Assert(t, cat.Modules()[0], qt.Equals, wfapi.ModuleName("example.com/module"))
					qt.Assert(t, cat.Modules()[1], qt.Equals, wfapi.ModuleName("example.com/module-two"))

					wareId, wareAddr, err := ws.GetCatalogWare(ref)
					qt.Assert(t, err, qt.IsNil)
					qt.Assert(t, wareId, qt.IsNotNil)
					qt.Assert(t, wareAddr, qt.IsNotNil)
					qt.Assert(t, wareId.Hash, qt.Equals, "abcd")
					qt.Assert(t, wareId.Packtype, qt.Equals, wfapi.Packtype("tar"))
					qt.Assert(t, wareAddr, qt.Contains, wfapi.WarehouseAddr("https://example.com/module/module-v1.0-x86_64.tgz"))
				}
			}
			t.Run("without abs path", check("home/user/"))
			t.Run("with abs path", check("/home/user/"))
		})
		t.Run("catalog-replay", func(t *testing.T) {
			fsys := fstest.MapFS{
				"home/user/.warpforge/catalog/example.com/module/_module.json": &fstest.MapFile{
					Mode: 0644,
					Data: []byte(moduleData),
				},
				"home/user/.warpforge/catalog/example.com/module/_releases/v1.0.json": &fstest.MapFile{
					Mode: 0644,
					Data: []byte(releaseData),
				},
				"home/user/.warpforge/catalog/example.com/module/_replays/zM5K3aX2vbXSjAMaFVBAAYccoNpf3h2mQkDZLFmD7pEZdUUWtsx1qk9Dh4KoPq7zmEdR1cQ.json": &fstest.MapFile{
					Mode: 0644,
					Data: []byte(replayData),
				},
				"home/user/.warpforge/catalog/example.com/module/_mirrors.json": &fstest.MapFile{
					Mode: 0644,
					Data: []byte(mirrorData),
				},
			}
			var err error
			ws, _, err := FindWorkspace(fsys, "", "home/user/")
			qt.Assert(t, err, qt.IsNil)
			qt.Assert(t, ws, qt.IsNotNil)

			cat, err := ws.OpenCatalog("")
			qt.Assert(t, err, qt.IsNil)
			_, err = cat.GetReplay(ref)
			qt.Assert(t, err, qt.IsNil)
		})

	})
}
