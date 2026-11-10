package io.github.aren55555.widgetd

import android.webkit.CookieManager
import io.flutter.embedding.android.FlutterActivity

class MainActivity : FlutterActivity() {
    // The screen's pairing lives in the WebView's cookie jar, which Android
    // writes to disk only now and then. Flush when leaving the foreground, so
    // the app being killed in the background doesn't unpair the screen.
    override fun onPause() {
        super.onPause()
        CookieManager.getInstance().flush()
    }
}
