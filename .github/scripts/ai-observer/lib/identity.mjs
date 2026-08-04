const EMAIL_PATTERN = /^(?!\.)(?!.*\.\.)([A-Z0-9_'+.-]*)[A-Z0-9_+-]@([A-Z0-9][A-Z0-9-]*\.)+[A-Z]{2,}$/i;

export const validEmail = (value) => {
  const email = value?.trim();
  return email && EMAIL_PATTERN.test(email) ? email : undefined;
};

export const validGithubId = (value) => {
  const githubId = value === undefined || value === null ? undefined : String(value).trim();
  return githubId && /^\d{1,30}$/.test(githubId) ? githubId : undefined;
};
