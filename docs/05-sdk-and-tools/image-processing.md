# Image Processing Tools

The project provides `libaiden_image.a` and `image_process` CLI for basic screenshot and image processing.

## Capabilities

- Crop;
- Auto-crop black bars;
- Scale;
- Rotate;
- Input supports PPM(P6) and formats supported by stb_image such as JPEG/PNG;
- Output supports `.ppm`, `.jpg`, `.jpeg`.

## CLI Usage

```text
image_process crop -b T,R,B,L -i INPUT -o OUTPUT [-q 1-100 for JPEG]
image_process crop_black_bars -i INPUT -o OUTPUT [-q JPEG quality]
image_process scale -s FACTOR -i INPUT -o OUTPUT [-q JPEG quality]
image_process rotate -d DEG -i INPUT -o OUTPUT [-q JPEG quality]
```

## Examples

Crop top, right, bottom, left margins:

```bash
./build/bin/image_process crop -b 10,20,10,20 -i input.jpg -o cropped.jpg -q 90
```

Crop black bars:

```bash
./build/bin/image_process crop_black_bars -i screenshot.jpg -o no-bars.jpg
```

Scale to 50%:

```bash
./build/bin/image_process scale -s 0.5 -i input.png -o small.jpg
```

Rotate 90 degrees:

```bash
./build/bin/image_process rotate -d 90 -i input.jpg -o rotated.jpg
```

## Library Targets

CMake targets:

- `aiden_image`: Static library;
- `image_process`: CLI.

Main source files:

- `src/image_process.cpp`
- `src/image_process.h`
- `src/image_process_cli.cpp`
