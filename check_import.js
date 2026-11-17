const fs = require('fs');
const fileContent = fs.readFileSync('plugins/velocity/src/main/java/io/github/amibamae/gsc/velocity/GscBridgePlugin.java', 'utf-8');

// Check for TlsMaterialManager import
const hasImport = fileContent.includes("import io.github.amibamae.gsc.velocity.tls.TlsMaterialManager");
console.log("Has TlsMaterialManager import:", hasImport);

// Check if it's used in BridgeHttpServer initialization
const initMatch = fileContent.match(/http = new BridgeHttpServer\([^)]+\)/);
if (initMatch) {
  console.log("BridgeHttpServer constructor call found:");
  console.log(initMatch[0]);
} else {
  console.log("BridgeHttpServer constructor call NOT found!");
}

// Find line numbers for relevant sections
const lines = fileContent.split('\n');
for (let i = 0; i < lines.length; i++) {
  const line = lines[i];
  if (line.includes('BridgeHttpServer')) {
    console.log(`Line ${i + 1}:`, line);
  }
}
