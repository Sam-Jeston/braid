// DOMPurify binds to the global window when it's imported, so the DOM has
// to exist first. jsdom is what DOMPurify itself is tested against; lighter
// DOMs mishandle sanitization and would make the security tests meaningless.
import { JSDOM } from "jsdom";

const { window } = new JSDOM("<!doctype html><html><body></body></html>", { url: "http://127.0.0.1/" });
Object.assign(globalThis, { window, document: window.document, navigator: window.navigator, location: window.location });
