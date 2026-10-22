function invalidExpression(reason) {
  return new SyntaxError(`invalid license expression: ${reason}`);
}

function tokenize(value) {
  const tokens = [];
  let offset = 0;
  while (offset < value.length) {
    if (/\s/.test(value[offset])) {
      offset += 1;
      continue;
    }
    if (value[offset] === "(" || value[offset] === ")") {
      tokens.push({ type: value[offset], value: value[offset] });
      offset += 1;
      continue;
    }

    const start = offset;
    while (offset < value.length && !/\s|[()]/.test(value[offset])) {
      offset += 1;
    }
    const word = value.slice(start, offset);
    if (word === "AND" || word === "OR") {
      tokens.push({ type: word, value: word });
      continue;
    }
    if (word === "WITH" || word.includes("+") || /^(?:DocumentRef-|LicenseRef-)/i.test(word)) {
      throw invalidExpression(`unsupported term ${JSON.stringify(word)}`);
    }
    if (!/^[A-Za-z0-9][A-Za-z0-9.-]*$/.test(word)) {
      throw invalidExpression(`unsupported term ${JSON.stringify(word)}`);
    }
    tokens.push({ type: "id", value: word });
  }
  return tokens;
}

function parse(value) {
  const tokens = tokenize(value);
  let index = 0;

  function current() {
    return tokens[index];
  }

  function primary() {
    const token = current();
    if (!token) {
      throw invalidExpression("expected a license ID or parenthesized expression");
    }
    if (token.type === "id") {
      index += 1;
      return { type: "id", value: token.value };
    }
    if (token.type !== "(") {
      throw invalidExpression(`unexpected token ${JSON.stringify(token.value)}`);
    }

    index += 1;
    const expression = orExpression();
    if (current()?.type !== ")") {
      throw invalidExpression("missing closing parenthesis");
    }
    index += 1;
    return expression;
  }

  function andExpression() {
    let expression = primary();
    while (current()?.type === "AND") {
      index += 1;
      expression = { type: "AND", left: expression, right: primary() };
    }
    return expression;
  }

  function orExpression() {
    let expression = andExpression();
    while (current()?.type === "OR") {
      index += 1;
      expression = { type: "OR", left: expression, right: andExpression() };
    }
    return expression;
  }

  const expression = orExpression();
  if (index !== tokens.length) {
    throw invalidExpression(`unexpected token ${JSON.stringify(current().value)}`);
  }
  return expression;
}

function isApproved(expression, allowedLicenses) {
  if (expression.type === "id") {
    return allowedLicenses.has(expression.value);
  }
  const left = isApproved(expression.left, allowedLicenses);
  const right = isApproved(expression.right, allowedLicenses);
  return expression.type === "AND" ? left && right : left || right;
}

export function assessLicenseDeclaration(value, allowedLicenses) {
  if (typeof value !== "string" || value.trim() === "") {
    throw invalidExpression("expected a non-empty string");
  }
  const declaration = value.trim();
  const expression = parse(declaration);
  return {
    approved: isApproved(expression, allowedLicenses),
    cycloneDXLicense:
      expression.type === "id"
        ? { license: { id: expression.value } }
        : { expression: declaration },
  };
}

// Classify canonical Go module license text without expanding the policy allowlist.
export function classifyGoLicenseText(value) {
  const content = value.toLowerCase();
  if (content.includes("mit license") || (content.includes("permission is hereby granted, free of charge") && content.includes("the software is provided \"as is\""))) {
    return "MIT";
  }
  if (content.includes("apache license") && content.includes("version 2.0")) {
    return "Apache-2.0";
  }
  if (content.includes("redistribution and use in source and binary forms")) {
    return "BSD-3-Clause";
  }
  if (/^mozilla public license version 2\.0\r?\n=+\r?\n/.test(content.trimStart()) && /https?:\/\/mozilla\.org\/MPL\/2\.0\//i.test(content)) {
    return "MPL-2.0";
  }
  return "UNKNOWN";
}
