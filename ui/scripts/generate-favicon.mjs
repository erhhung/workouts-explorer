import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const output = resolve(root, "public/favicon.ico");
const sizes = [16, 32, 48];

function rgb(hex) {
  return [1, 3, 5].map((offset) => Number.parseInt(hex.slice(offset, offset + 2), 16));
}

function render(size) {
  const scale = 4;
  const width = size * scale;
  const source = new Uint8Array(width * width * 4);
  const canvas = rgb("#0b1514");
  const amber = rgb("#e9a852");
  const blue = rgb("#73b6c7");
  const waypoint = rgb("#ffc16b");

  function paint(x, y, fill) {
    if (x < 0 || y < 0 || x >= width || y >= width) return;
    const offset = (y * width + x) * 4;
    source.set(fill, offset);
    source[offset + 3] = 255;
  }

  function circle(cx, cy, radius, fill) {
    for (let y = Math.floor(cy - radius); y <= Math.ceil(cy + radius); y += 1) {
      for (let x = Math.floor(cx - radius); x <= Math.ceil(cx + radius); x += 1) {
        if ((x - cx) ** 2 + (y - cy) ** 2 <= radius ** 2) paint(x, y, fill);
      }
    }
  }

  const inset = width * 0.03;
  const radius = width * 0.23;
  for (let y = 0; y < width; y += 1) {
    for (let x = 0; x < width; x += 1) {
      const centerX = Math.min(Math.max(x, inset + radius), width - inset - radius);
      const centerY = Math.min(Math.max(y, inset + radius), width - inset - radius);
      if ((x - centerX) ** 2 + (y - centerY) ** 2 <= radius ** 2) paint(x, y, canvas);
    }
  }

  function curve(start, controlA, controlB, end, fill) {
    let previous = start;
    for (let step = 1; step <= 80; step += 1) {
      const t = step / 80;
      const inverse = 1 - t;
      const next = [
        inverse ** 3 * start[0] + 3 * inverse ** 2 * t * controlA[0] + 3 * inverse * t ** 2 * controlB[0] + t ** 3 * end[0],
        inverse ** 3 * start[1] + 3 * inverse ** 2 * t * controlA[1] + 3 * inverse * t ** 2 * controlB[1] + t ** 3 * end[1],
      ];
      const distance = Math.max(1, Math.ceil(Math.hypot(next[0] - previous[0], next[1] - previous[1])));
      for (let segment = 0; segment <= distance; segment += 1) {
        const ratio = segment / distance;
        circle(previous[0] + (next[0] - previous[0]) * ratio, previous[1] + (next[1] - previous[1]) * ratio, width * 0.039, fill);
      }
      previous = next;
    }
  }

  const point = (x, y) => [x * width, y * width];
  curve(point(0.16, 0.63), point(0.19, 0.29), point(0.43, 0.17), point(0.61, 0.36), amber);
  curve(point(0.61, 0.36), point(0.77, 0.54), point(0.58, 0.82), point(0.36, 0.75), amber);
  curve(point(0.31, 0.72), point(0.49, 0.91), point(0.8, 0.74), point(0.81, 0.49), blue);
  curve(point(0.81, 0.49), point(0.82, 0.31), point(0.67, 0.21), point(0.53, 0.27), blue);
  circle(width * 0.77, width * 0.2, width * 0.085, canvas);
  circle(width * 0.77, width * 0.2, width * 0.057, waypoint);

  const rgba = Buffer.alloc(size * size * 4);
  for (let y = 0; y < size; y += 1) {
    for (let x = 0; x < size; x += 1) {
      for (let channel = 0; channel < 4; channel += 1) {
        let sum = 0;
        for (let sy = 0; sy < scale; sy += 1) {
          for (let sx = 0; sx < scale; sx += 1) sum += source[(((y * scale + sy) * width + x * scale + sx) * 4) + channel];
        }
        rgba[((y * size + x) * 4) + channel] = Math.round(sum / scale ** 2);
      }
    }
  }

  const bitmapHeader = Buffer.alloc(40);
  bitmapHeader.writeUInt32LE(40, 0);
  bitmapHeader.writeInt32LE(size, 4);
  bitmapHeader.writeInt32LE(size * 2, 8);
  bitmapHeader.writeUInt16LE(1, 12);
  bitmapHeader.writeUInt16LE(32, 14);
  bitmapHeader.writeUInt32LE(size * size * 4, 20);
  const pixels = Buffer.alloc(size * size * 4);
  const maskRowBytes = Math.ceil(size / 32) * 4;
  const mask = Buffer.alloc(maskRowBytes * size);
  for (let y = 0; y < size; y += 1) {
    const sourceY = size - y - 1;
    for (let x = 0; x < size; x += 1) {
      const from = (sourceY * size + x) * 4;
      const to = (y * size + x) * 4;
      pixels[to] = rgba[from + 2];
      pixels[to + 1] = rgba[from + 1];
      pixels[to + 2] = rgba[from];
      pixels[to + 3] = rgba[from + 3];
      if (rgba[from + 3] < 128) mask[y * maskRowBytes + Math.floor(x / 8)] |= 0x80 >> (x % 8);
    }
  }
  return Buffer.concat([bitmapHeader, pixels, mask]);
}

const images = sizes.map(render);
const header = Buffer.alloc(6 + images.length * 16);
header.writeUInt16LE(0, 0);
header.writeUInt16LE(1, 2);
header.writeUInt16LE(images.length, 4);
let imageOffset = header.length;
images.forEach((image, index) => {
  const entry = 6 + index * 16;
  header[entry] = sizes[index];
  header[entry + 1] = sizes[index];
  header.writeUInt16LE(1, entry + 4);
  header.writeUInt16LE(32, entry + 6);
  header.writeUInt32LE(image.length, entry + 8);
  header.writeUInt32LE(imageOffset, entry + 12);
  imageOffset += image.length;
});
const generated = Buffer.concat([header, ...images]);

if (process.argv.includes("--check")) {
  if (!generated.equals(readFileSync(output))) throw new Error("favicon.ico is out of date; run npm run generate:favicon");
} else {
  mkdirSync(dirname(output), { recursive: true });
  writeFileSync(output, generated);
}
