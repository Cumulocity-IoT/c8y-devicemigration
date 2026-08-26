/**
 * The tests that can run without a browser: the API client, the progress
 * parsing and the wizard's rules. All three are plain TypeScript on purpose -
 * see src/api/client.ts - so this needs neither Angular nor a DOM.
 */
module.exports = {
  preset: 'ts-jest',
  testEnvironment: 'node',
  roots: ['<rootDir>/src'],
  testMatch: ['**/*.spec.ts'],
  clearMocks: true
};
