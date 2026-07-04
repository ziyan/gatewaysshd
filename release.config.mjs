export default {
  branches: ['master'],
  tagFormat: 'v${version}',
  plugins: [
    './scripts/release/commit-analyzer-no-major.mjs',
    '@semantic-release/release-notes-generator',
    './scripts/release/changelog-promote.mjs',
    [
      '@semantic-release/git',
      {
        assets: ['CHANGELOG.md', 'version.go'],
        message: 'chore(release): ${nextRelease.version}\n\n${nextRelease.notes}',
      },
    ],
  ],
};
