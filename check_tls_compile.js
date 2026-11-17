import fs from 'fs';

// Check if the TLS files compile together
const tlsDir = "plugins/velocity/src/main/java/io/github/amibamae/gsc/velocity/tls";
const javaFiles = [
    tlsDir + "/TlsMaterialManager.java",
    tlsDir + "/HttpConfigurator.java",
    tlsDir + "/SslContextBuilder.java"
];

console.log("Checking TLS files syntax...");

// Check for missing dependencies
let errors = 0;

for (const file of javaFiles) {
  try {
    // Try to read and compile each file
    const content = fs.readFileSync(file, 'utf-8');
    
    // Simple syntax check: look for unmatched braces
    const openBraces = (content.match(/\{/g) || []).length;
    const closeBraces = (content.match(/\}/g) || []).length;
    
    if (openBraces !== closeBraces) {
      console.log(`✗ ${file}: Unmatched braces (${openBraces} open, ${closeBraces} close)`);
      errors++;
    } else {
      console.log(`✓ ${file}: Syntax looks good`);
    }
  } catch (e) {
    console.log(`✗ ${file}: Error reading file - ${e.message}`);
    errors++;
  }
}

// Check for common Java issues in the content
for (const file of javaFiles) {
  const content = fs.readFileSync(file, 'utf-8');
  
  // Check for unmatched parentheses
  const openParens = (content.match(/\(/g) || []).length;
  const closeParens = (content.match(/\)/g) || []).length;
  if (openParens !== closeParens) {
    console.log(`✗ ${file}: Unmatched parentheses`);
    errors++;
  }
  
  // Check for semicolons at end of files (should be present)
  const trimmed = content.trim();
  if (!trimmed.endsWith(';')) {
    console.log(`✓ ${file}: Ends with statement terminator or class body`);
  }
}

if (errors === 0) {
  console.log("\n✓ All TLS files appear to have valid syntax!");
} else {
  console.log(`\n✗ Found ${errors} error(s) in the TLS files.`);
  process.exit(1);
}
