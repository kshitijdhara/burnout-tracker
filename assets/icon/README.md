# Icons

The app icon is a slice of burnt toast, which is to say burned out.

## App icon (`app/`)

Five 1024×1024 SVG layers, bottom to top:

| Layer | What it is |
|---|---|
| `0-background.svg` | Warm yellow-to-orange gradient |
| `1-bread.svg` | The crust and its shadow |
| `2-burn.svg` | The charred middle |
| `3-face.svg` | Dazed ✕✕ eyes, a wobbly mouth and blush |
| `4-smoke.svg` | Two wisps of smoke |

To build the macOS icon, open Icon Composer (Xcode 26), drag the layers in
bottom to top, use `0-background.svg` as the background fill, and export the
`.icon` file. `icon-1024.png` is the flattened icon, for platforms and
stores that want a single image.

## Tray (`tray/`)

The daemon builds these into the binary (`assets.go`).

- **macOS menu bar:** template images, black with transparency, which
  macOS recolours for light and dark menu bars. Each is 18×18 pt, with
  `@2x` PNGs for Retina.
- **Windows and Linux:** colour icons, which show on light and dark
  taskbars and panels, at 16, 32 and 64 px. Windows gets them wrapped in
  an `.ico`.

| Files | When it shows |
|---|---|
| `trayTemplate`, `trayColor` | Recording: the toast with its dazed face |
| `trayPausedTemplate`, `trayPausedColor` | Paused or private hours: pause bars |

Edit the SVGs, then render the PNGs from them at those sizes.
