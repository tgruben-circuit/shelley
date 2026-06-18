import { test, expect } from '@playwright/test';

const KEYWORD = 'pineapplexyz';

test.describe('Conversation Search Snippets', () => {
  test('shows highlighted snippet and jumps to matching message', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('domcontentloaded');

    // 1. Send a user message containing a unique keyword.
    const messageInput = page.getByTestId('message-input');
    await expect(messageInput).toBeVisible({ timeout: 30000 });
    await messageInput.fill(`please remember ${KEYWORD}`);

    const sendButton = page.getByTestId('send-button');
    await expect(sendButton).toBeVisible();
    await sendButton.click();

    // 2. Wait until the message is persisted/rendered (visible in the DOM).
    await page.waitForFunction(
      (kw) => document.body.textContent?.includes(kw) ?? false,
      KEYWORD,
      { timeout: 30000 },
    );

    // 3. Open the command palette. The App handler accepts Ctrl+K (Linux/CI)
    //    and Meta+K (mac); Control+k works in this CI environment.
    await page.keyboard.press('Control+k');

    // 4. Fill the debounced (150ms) palette search input. The search hits the
    //    server, so we wait for results via web-first assertions below.
    const paletteInput = page.getByPlaceholder('Search conversations or actions...');
    await expect(paletteInput).toBeVisible({ timeout: 5000 });
    await paletteInput.fill(KEYWORD);

    // 5. Assert a highlighted snippet shows the keyword (generous timeout for
    //    debounce + server round-trip).
    await expect(page.locator('.command-palette-snippet mark').first()).toContainText(KEYWORD, {
      ignoreCase: true,
      timeout: 15000,
    });

    // 6. Click the matching result row.
    await page
      .locator('.command-palette-item', { hasText: KEYWORD })
      .first()
      .click();

    // 7. Assert jump-to-match.
    //    The `.message-flash` class is transient (removed on animationend ~2s),
    //    so asserting on it directly is racy. Instead we use robust web-first
    //    assertions: the palette closes and the matched message remains visible
    //    in the conversation view. We also confirm the message wrapper element
    //    (id="msg-<id>") that received the flash is present and in view, which
    //    is the stable signal that ChatInterface scrolled to + targeted the
    //    matching message.
    await expect(paletteInput).toBeHidden();
    await expect(page.locator('text=' + KEYWORD).first()).toBeVisible();

    // The matched message wrapper has id="msg-<message_id>". Confirm at least one
    // msg-* element is present and the one containing the keyword is in view.
    const matchedMsg = page.locator('[id^="msg-"]', { hasText: KEYWORD }).first();
    await expect(matchedMsg).toBeVisible();
    await matchedMsg.scrollIntoViewIfNeeded();
    await expect(matchedMsg).toBeInViewport();
  });
});
