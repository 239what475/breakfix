declare module "qrcode";

interface ImportMetaEnv {
  readonly VITE_DOCS_ORIGIN?: string;
}

interface ImportMeta {
  readonly env: ImportMetaEnv;
}
