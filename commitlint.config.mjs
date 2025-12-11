import { RuleConfigSeverity, } from "@commitlint/types";

const base = {
    extends: ['@commitlint/config-conventional'],
    ignores: [
        // ignore commits from dependabot
        (commit) => /Signed-off-by: dependabot\[bot\]/.test(commit),
    ],
    rules: {
        "type-enum": [
            RuleConfigSeverity.Error,
            "always",
            [
                "build",
                "ci",
                "docs",
                "feat",
                "fix",
                "perf",
                "refactor",
                "style",
                "test",
                "revert",
                "gomod",
                "chore",
            ],
        ],

        // leading blanks aren't super useful
        "body-leading-blank": [RuleConfigSeverity.Disabled],
        // HACK: this detects false positives, see https://github.com/conventional-changelog/commitlint/issues/3129
        "footer-leading-blank": [RuleConfigSeverity.Disabled],
    },
};

let config;
if (process.env.PR) {
    config = {
        ...base,
        rules: {
            ...base.rules,

            "header-max-length": [RuleConfigSeverity.Error, "always", 75],
            "body-max-line-length": [RuleConfigSeverity.Error, "always", 75],
            "footer-max-line-length": [RuleConfigSeverity.Error, "always", 75],
        },
    };
} else {
    config = {
        ...base,
        rules: {
            ...base.rules,

            "header-max-length": [RuleConfigSeverity.Error, "always", 74],
            "body-max-line-length": [RuleConfigSeverity.Error, "always", 74],
            "footer-max-line-length": [RuleConfigSeverity.Error, "always", 74],

            "signed-off-by": [RuleConfigSeverity.Error, "always", "Signed-off-by:"],
        },
    };
}

export default config;
