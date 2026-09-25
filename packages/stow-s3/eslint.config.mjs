import eslint from "@eslint/js";
import importPlugin from "eslint-plugin-import";
import globals from "globals";
import tseslint from "typescript-eslint";

export default tseslint.config(
  {
    ignores: ["dist/**", "node_modules/**", "coverage/**"],
  },
  eslint.configs.recommended,
  ...tseslint.configs.recommended,
  {
    // Build and maintenance scripts, and the published stow-doctor entry point,
    // run in Node rather than the browser. Without this they are linted with no
    // globals, so every process and console reference is reported as undefined.
    files: ["scripts/**/*.mjs", "bin/**/*.mjs"],
    languageOptions: {
      globals: { ...globals.node },
      parserOptions: { ecmaVersion: "latest", sourceType: "module" },
    },
    rules: {
      "no-console": "off",
    },
  },
  {
    files: ["src/**/*.ts", "test/**/*.ts"],
    languageOptions: {
      globals: { ...globals.node },
      parserOptions: { ecmaVersion: "latest", sourceType: "module" },
    },
    plugins: { import: importPlugin },
    rules: {
      complexity: ["warn", { max: 15 }],
      "max-depth": ["warn", { max: 3 }],
      "max-params": ["warn", { max: 5 }],
      "max-lines-per-function": [
        "warn",
        { max: 200, skipBlankLines: true, skipComments: true },
      ],
      "no-console": "warn",
      "no-return-await": "warn",
      "@typescript-eslint/no-explicit-any": "warn",
      "@typescript-eslint/no-non-null-assertion": "warn",
      "@typescript-eslint/consistent-type-imports": [
        "warn",
        { prefer: "type-imports", fixStyle: "inline-type-imports" },
      ],
      "@typescript-eslint/no-unused-vars": [
        "warn",
        {
          argsIgnorePattern: "^_",
          varsIgnorePattern: "^_",
          caughtErrorsIgnorePattern: "^_",
          destructuredArrayIgnorePattern: "^_",
        },
      ],
      "@typescript-eslint/ban-ts-comment": [
        "error",
        {
          "ts-expect-error": "allow-with-description",
          "ts-ignore": true,
          "ts-nocheck": true,
        },
      ],
      "import/no-duplicates": "error",
    },
  },
);
