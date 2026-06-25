import type { CodegenConfig } from "@graphql-codegen/cli";

// Generates typed documents for every graphql(...) call under src/ from the
// server's schema, so a field renamed in Go is a compile error here.
const config: CodegenConfig = {
  schema: "../internal/graph/schema.graphqls",
  documents: ["src/**/*.{ts,tsx}", "!src/gql/**"],
  generates: {
    "./src/gql/": {
      preset: "client",
      config: {
        scalars: { JSON: "unknown", Time: "string" },
        useTypeImports: true,
        enumsAsTypes: true,
      },
      presetConfig: { fragmentMasking: false },
    },
  },
};

export default config;
