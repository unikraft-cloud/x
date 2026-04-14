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

        // enforce sentence case for subject
        "subject-case": [RuleConfigSeverity.Error, "always", ["sentence-case"]],
        // leading blanks aren't super useful
        "body-leading-blank": [RuleConfigSeverity.Disabled],
        // HACK: this detects false positives, see https://github.com/conventional-changelog/commitlint/issues/3129
        "footer-leading-blank": [RuleConfigSeverity.Disabled],

        // line lengths
        "header-max-length": [RuleConfigSeverity.Error, "always", 74],
        "body-max-line-length": [RuleConfigSeverity.Error, "always", 74],
        "footer-max-line-length": [RuleConfigSeverity.Error, "always", 74],
    },
};

let config;
if (process.env.PR) {
    config = {
        ...base,
        rules: {
            ...base.rules,
            "header-max-length": [RuleConfigSeverity.Error, "always", 70],
        },
    };
} else {
    config = {
        ...base,
        rules: {
            ...base.rules,
            "signed-off-by": [RuleConfigSeverity.Error, "always", "Signed-off-by:"],
        },
    };
}

export default config;
