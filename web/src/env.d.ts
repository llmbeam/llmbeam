export {}

declare global {
  interface Window {
    __LLMBEAM_FETCH__?: typeof fetch
  }
}
