---
name: image-assets
description: Create, convert and optimise images and visual assets - resize, crop, compress, convert (PNG/JPEG/WebP/AVIF/SVG/ICO), make favicons and app icons, social/OG images, product photos, screenshots, simple SVG logos and icons, background removal - with ImageMagick, ffmpeg, Python or SVG by hand. Use for any image or asset file task.
tags: [images, assets, svg, imagemagick, web, design]
version: 1
---

# Image and asset handling

Check tools first (`magick -version` or `convert -version`, `ffmpeg -version`, `python -c "import PIL"`); offer
install_tool if missing (`winget install ImageMagick.ImageMagick`, `brew install imagemagick`,
`sudo pacman -S imagemagick`). Never overwrite originals: write to a new file or an `out/` folder.

## Common recipes (ImageMagick 7: `magick`; v6: `convert`)

| Task | Command |
|---|---|
| Info (size, format, colour) | `magick identify -verbose in.png \| head -20` |
| Resize to width 1200, keep ratio, only shrink | `magick in.jpg -resize '1200x>' out.jpg` |
| Crop to 16:9 centred | `magick in.jpg -gravity center -crop 16:9 +repage out.jpg` |
| Square thumbnail 400px (fill + crop) | `magick in.jpg -resize 400x400^ -gravity center -extent 400x400 thumb.jpg` |
| Web JPEG | `magick in.png -strip -quality 82 -sampling-factor 4:2:0 -interlace JPEG out.jpg` |
| WebP / AVIF | `magick in.png -quality 80 out.webp`; `magick in.png -quality 55 out.avif` |
| Batch convert a folder | `magick mogrify -path out -format webp -quality 80 *.png` |
| Favicon .ico | `magick logo.png -define icon:auto-resize=16,32,48,64,256 favicon.ico` |
| Transparent → white bg | `magick in.png -background white -alpha remove -alpha off out.jpg` |
| Remove plain white background | `magick in.png -fuzz 8% -transparent white out.png` |
| Add padding | `magick in.png -gravity center -background none -extent 120%x120% out.png` |
| Contact sheet | `magick montage *.jpg -tile 4x -geometry 300x300+8+8 sheet.jpg` |
| Video frame / GIF → MP4 | `ffmpeg -ss 00:00:03 -i v.mp4 -frames:v 1 frame.png`; `ffmpeg -i a.gif -movflags faststart -pix_fmt yuv420p a.mp4` |

Python (Pillow) when logic is needed: `Image.open(p).convert("RGB").thumbnail((1200,1200)); im.save(out, quality=82, optimize=True)`.

## Sizes to know

- Social/OG image: 1200×630 (text inside the central 1000×500). X/Twitter: 1600×900. Instagram: 1080×1080 / 1080×1350 / story 1080×1920.
- Favicons: `favicon.ico` (16/32/48), `apple-touch-icon.png` 180×180, PWA icons 192 and 512 (+ maskable with 10% safe padding).
- App icons: Windows `.ico` up to 256; macOS `.icns` from a 1024 PNG (`iconutil`); Linux 512 PNG/SVG.
- Web images: serve ≤ 2× the displayed width; hero ≤ 250 KB, thumbnails ≤ 60 KB; always set width/height in HTML.

## SVG by hand (logos, icons, simple illustrations)

- `viewBox` set, no fixed width/height for icons; `currentColor` for strokes/fills so CSS can theme it.
- Simple geometry on a grid (e.g. 24×24, stroke 1.5-2, round or square caps consistent with the brand). Blaze icons use square caps and no rounded corners.
- Title for accessibility: `<svg role="img" aria-label="…">`; decorative: `aria-hidden="true"`.
- Optimise: remove editor metadata; `npx svgo in.svg -o out.svg` if available.
- Text in logos: convert to paths or use a web-safe font; check it at 16px and 512px.

## Product and photo cleanup

Straighten and crop; consistent background (white or one brand colour) and aspect ratio across a set; same lighting/white balance (`-auto-level`, `-modulate 100,110` for a little saturation); compress; descriptive filenames (`warm-desk-lamp-front.webp`) and alt text.

## Check

- [ ] Open the output and look at it (read the image file) - no stretching, banding, halos or cut-off text.
- [ ] File sizes and dimensions reported to the user; originals untouched.
- [ ] Licences: only use images the user owns, generated, or free-licensed (note the source and licence).
