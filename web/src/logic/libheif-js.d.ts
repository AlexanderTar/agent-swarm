declare module "libheif-js/wasm-bundle" {
  interface HeifImage {
    get_width(): number;
    get_height(): number;
    display(
      target: { data: Uint8ClampedArray; width: number; height: number },
      done: (out: unknown) => void,
    ): void;
  }
  const libheif: { HeifDecoder: new () => { decode(bytes: Uint8Array): HeifImage[] } };
  export default libheif;
}
