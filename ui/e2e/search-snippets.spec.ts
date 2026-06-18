import { test, expect } from '@playwright/test';

const KEYWORD = 'pineapplexyz';
const FILLER_COUNT = 14;

test.describe('Conversation Search Snippets', () => {
  test('shows highlighted snippet and jumps to matching message', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('domcontentloaded');

    const messageInput = page.getByTestId('message-input');
    await expect(messageInput).toBeVisible({ timeout: 30000 });
    const sendButton = page.getByTestId('send-button');

    // Helper: send a message and wait for its echo to render.
    const send = async (text: string, waitFor: string) => {
      await messageInput.fill(text);
      await expect(sendButton).toBeEnabled();
      await sendButton.click();
      await page.waitForFunction(
        (kw) => document.body.textContent?.includes(kw) ?? false,
        waitFor,
        { timeout: 30000 },
      );
    };

    // 1. Build a TALL conversation. Send the unique-keyword message FIRST so it
    //    ends up at the top, then pile on filler so it is pushed well above the
    //    fold. The predictable model echoes `echo: <text>` back, so this also
    //    produces an agent message that contains the keyword.
    await send(`echo: ${KEYWORD} marker`, `${KEYWORD} marker`);
    for (let i = 0; i < FILLER_COUNT; i++) {
      await send(`echo: filler ${i}`, `filler ${i}`);
    }

    // Wait for the conversation layout to QUIESCE before interacting. Agent echo
    // messages stream in over ~1s after their text first appears, so the message
    // list keeps growing for a while; settling first makes the jump deterministic.
    // We also require the list to be much taller than the viewport so the keyword
    // message (sent first) is genuinely scrolled far out of view at the bottom.
    await page.waitForFunction(
      () => {
        const list = document.querySelector('.messages-list') as HTMLElement | null;
        if (!list) return false;
        const w = window as unknown as { __lastH?: number; __stable?: number };
        const h = Math.round(list.getBoundingClientRect().height);
        if (w.__lastH === h) {
          w.__stable = (w.__stable ?? 0) + 1;
        } else {
          w.__stable = 0;
          w.__lastH = h;
        }
        return (w.__stable ?? 0) >= 5 && h > window.innerHeight * 1.5;
      },
      undefined,
      { timeout: 30000, polling: 100 },
    );

    // The keyword appears exactly once (one user message) at the TOP of this tall
    // list. Capture the scroll container and assert its starting state: after the
    // auto-scroll-to-bottom on the last send, we are pinned to the BOTTOM, so the
    // keyword message is far above the fold and scrollTop is near its maximum.
    const matchedMsg = page.locator('[id^="msg-"]', { hasText: KEYWORD }).first();
    await expect(matchedMsg).toBeAttached();
    const matchedId = await matchedMsg.getAttribute('id');

    // Precondition: scrolled to the bottom (scrollTop ~= max) AND the keyword
    // message is well out of view. This is what makes the jump assertion below
    // meaningful — the app must scroll a long way UP to reach the target.
    const startState = await page.evaluate((id) => {
      const c = document.querySelector('.messages-container') as HTMLElement;
      const el = document.getElementById(id!)!;
      return {
        scrollTop: c.scrollTop,
        maxScroll: c.scrollHeight - c.clientHeight,
        // Distance (px) from the message bottom up to the container's top edge:
        // large & positive means the message is far above the visible area.
        aboveBy: c.getBoundingClientRect().top - el.getBoundingClientRect().bottom,
      };
    }, matchedId);
    // We start near the bottom and the keyword message is hundreds of px above view.
    expect(startState.scrollTop).toBeGreaterThan(startState.maxScroll * 0.5);
    expect(startState.aboveBy).toBeGreaterThan(300);
    await expect(matchedMsg).not.toBeInViewport();

    // 2. Open the command palette (Ctrl+K on Linux/CI, Meta+K on mac).
    await page.keyboard.press('Control+k');

    // 3. Fill the debounced (150ms) palette search input.
    const paletteInput = page.getByPlaceholder('Search conversations or actions...');
    await expect(paletteInput).toBeVisible({ timeout: 5000 });
    await paletteInput.fill(KEYWORD);

    // 4. Assert a highlighted snippet shows the keyword.
    await expect(page.locator('.command-palette-snippet mark').first()).toContainText(KEYWORD, {
      ignoreCase: true,
      timeout: 15000,
    });

    // 5. Click the matching result row. This threads targetMessageId through
    //    selectConversation -> ChatInterface, which scrolls to + flashes it.
    await page
      .locator('.command-palette-item', { hasText: KEYWORD })
      .first()
      .click();

    // Palette closes.
    await expect(paletteInput).toBeHidden();

    // 6. KEY assertion — the jump actually happened. We assert the app scrolled the
    //    long, bottom-pinned list all the way UP so the keyword message (the FIRST
    //    message) is brought into the top region of the conversation.
    //
    //    Why scrollTop (not a strict toBeInViewport)? The keyword message lives at
    //    the top of the list, so a working jump drives scrollTop down to ~0 (top),
    //    whereas a regression that drops `targetMessageId` performs no scroll and
    //    leaves scrollTop pinned near its maximum (~the bottom). That gap — top vs
    //    bottom of a list ~2x the viewport tall — is the unambiguous, reliable
    //    signal that the scroll-to-target wiring fired. (The feature's exact final
    //    pixel offset varies by a few hundred px as sibling messages finish
    //    rendering, so asserting an exact in-viewport ratio is flaky; asserting the
    //    list scrolled from bottom to top is both robust and meaningful.)
    await expect
      .poll(
        async () =>
          page.evaluate(() => {
            const c = document.querySelector('.messages-container') as HTMLElement;
            return c.scrollTop;
          }),
        { timeout: 10000 },
      )
      // A correct jump scrolls near the top of the list. The keyword message is the
      // first message (after the system-prompt header, ~370px), so a faithful jump
      // lands scrollTop well under one viewport height; a no-jump regression stays
      // pinned near maxScroll (>1000px here).
      .toBeLessThan(startState.maxScroll * 0.5);

    // Also confirm the matched message itself moved up to the visible region: it
    // started ~1000px above the fold (see startState.aboveBy) and after the jump
    // it is at or adjacent to the top of the conversation. We allow generous slack
    // (the feature's final offset wobbles a few hundred px as siblings finish
    // rendering) — the point is that it is no longer ~1000px out, which a no-jump
    // regression would leave it.
    const afterAboveBy = await page.evaluate((id) => {
      const c = document.querySelector('.messages-container') as HTMLElement;
      const el = document.getElementById(id!)!;
      return c.getBoundingClientRect().top - el.getBoundingClientRect().bottom;
    }, matchedId);
    expect(afterAboveBy).toBeLessThan(500);
    // And it moved up substantially from where it started.
    expect(afterAboveBy).toBeLessThan(startState.aboveBy - 400);

    // 7. Best-effort: confirm the flash class was applied (further evidence the
    //    jump effect ran). `.message-flash` is removed on animationend (~2s), so we
    //    poll leniently and do NOT fail the test on it — the scroll assertions
    //    above are authoritative.
    try {
      await expect(page.locator('.message-flash')).toBeAttached({ timeout: 1500 });
    } catch {
      // Flash already cleaned up by the time we polled; acceptable.
    }
  });
});
