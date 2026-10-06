import React from 'react'
import ReactDOM from 'react-dom/client'

// Fonts are bundled, not fetched: the app must look right offline.
import '@fontsource/inter/400.css'
import '@fontsource/inter/500.css'
import '@fontsource/inter/600.css'
import '@fontsource/inter/700.css'
import '@fontsource/vazirmatn/400.css'
import '@fontsource/vazirmatn/500.css'
import '@fontsource/vazirmatn/600.css'
import '@fontsource/vazirmatn/700.css'

import App from './App'
import './index.css'

const container = document.getElementById('root')
if (!container) {
  throw new Error('root element is missing from index.html')
}

ReactDOM.createRoot(container).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
)
