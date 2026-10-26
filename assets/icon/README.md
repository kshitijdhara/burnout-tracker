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

## Menu bar (`tray/`)

Template images: black with transparency, which macOS recolours for light
and dark menu bars. Each is 18×18 pt, with `@2x` PNGs for Retina.

| File | When it shows |
|---|---|
| `trayTemplate` | Collecting: the toast with its dazed face |
| `trayPausedTemplate` | Paused: an outlined toast with pause bars |

Edit the SVGs; the PNGs are rendered from them.
